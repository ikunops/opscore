package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuDong999/opscore/internal/protection"
)

// historyExportFileBase is the on-disk snapshot prefix (Phase 34). One Tick
// computes ONE shared snapshot base identity alert-transitions-<ts>[.N]; every
// format appends its own extension, so a snapshot's artifacts share the same
// collision ordinal: alert-transitions-<ts>[.N].<fmt> (R162/B — base-level
// collision retry, never per-format retry suffixes).
const historyExportFileBase = "alert-transitions"

// HistoryExportConfig configures the Phase 34 scheduled periodic export of the
// durable alert-transition history to local files. The scheduler is OPT-IN: the
// composition root only constructs it when BOTH a positive interval and a
// destination dir are supplied; otherwise it stays nil and nothing is written.
type HistoryExportConfig struct {
	// Store is the durable alert-transition store. REQUIRED: a nil Store is a
	// fail-fast config error at New time (R160 — durable-only export never
	// silently degrades to a no-op scheduler). A configured store that later
	// returns a ReadAll error/corrupt still makes the tick SKIP at runtime
	// (degraded store), but nil is never accepted.
	Store protection.AlertTransitionStore
	// Dir is the destination directory for snapshot files. Required when enabled.
	Dir string
	// Interval is the tick period. Required > 0 when enabled.
	Interval time.Duration
	// Formats lists the export encodings to materialize each tick. All formats
	// share ONE snapshot base identity per tick; a per-format write failure is
	// reported explicitly (P34-I2 — no faked success). Only "json" and "csv"
	// are accepted.
	Formats []string
	// Retain is the local snapshot retention cap. 0 keeps ALL snapshots; >0
	// keeps at most Retain newest SNAPSHOTS. One scheduled export (one
	// ExportedAt) is ONE snapshot regardless of how many formats (.json/.csv)
	// it materializes; a snapshot and all its artifact files are removed together
	// (P34-I6 bounded local retention, corrected to snapshot units per R161/B).
	// Default 96.
	Retain int
	// Logger receives lifecycle/error diagnostics. May be nil.
	Logger *slog.Logger
	// Clock is the scheduler-side wall clock (LastRunAt / prune / tests only).
	// ExportedAt is the STORE's provenance and is NEVER derived from Clock
	// (P34-CLOCK-1). Defaults to time.Now.
	Clock func() time.Time
	// SignKeyPath is the Phase 37 Ed25519 signing key (PKCS#8 PEM or raw 64
	// bytes). EMPTY = signing disabled: manifests are published as schema v2
	// exactly as before (default behaviour is unchanged).
	SignKeyPath string
	// SignKeyID is an OPTIONAL consistency assertion only. key_id is ALWAYS
	// derived from the private key; a non-empty value that disagrees with the
	// derived id is a fail-fast configuration error (never "sign with A, claim B").
	SignKeyID string
	// TrustKeyPaths are the trusted Ed25519 public keys used by the verify
	// surface. They form the trust anchor and are loaded from an independent
	// configuration source — never from a manifest, and never from the signing key.
	TrustKeyPaths []string
	// LedgerCapacity bounds the Phase 39 chain digest ledger
	// (`chain-ledger.jsonl`), which is retained INDEPENDENTLY of the snapshot
	// retention so a legal prune no longer destroys the chain proof. Older
	// entries are dropped first, which only shrinks the verifiable range.
	// 0 disables the cap (keep everything).
	LedgerCapacity int
}

// HistoryExportStatus is the read-only scheduler state surfaced via
// GET /management/v1/protection/alerts/history/export/scheduler.
type HistoryExportStatus struct {
	Enabled               bool       `json:"enabled"`
	Running               bool       `json:"running"`
	LastRunAt             *time.Time `json:"last_run_at,omitempty"`      // scheduler attempt time (scheduler clock)
	LastExportedAt        *time.Time `json:"last_exported_at,omitempty"` // store provenance (res.ExportedAt)
	LastError             string     `json:"last_error,omitempty"`
	PruneError            string     `json:"prune_error,omitempty"`
	ManifestError         string     `json:"manifest_error,omitempty"`          // Phase 35: manifest publish failure (artifacts stay published)
	PublicationStateError string     `json:"publication_state_error,omitempty"` // Phase 35 M7: watermark unavailable ⇒ fail-closed skip
	SignatureError        string     `json:"signature_error,omitempty"`         // Phase 37: signing failure (fail-closed, manifest not published)
	ChainError            string     `json:"chain_error,omitempty"`             // Phase 38: chain extension refused (fail-closed)
	LedgerError           string     `json:"ledger_error,omitempty"`            // Phase 39: ledger append/backfill problem (manifest stays published)
	SigningEnabled        bool       `json:"signing_enabled"`                   // Phase 37: a signing key is configured
	SignerKeyID           string     `json:"signer_key_id,omitempty"`           // Phase 37: derived (never configured) key id
	TrustedKeys           int        `json:"trusted_keys"`                      // Phase 37: size of the independent trust anchor
	SkipCount             int64      `json:"skip_count"`
	Published             int64      `json:"published"`
	Failed                int64      `json:"failed"`
	Dir                   string     `json:"dir,omitempty"`
	Interval              string     `json:"interval,omitempty"`
	Formats               []string   `json:"formats,omitempty"`
	Retain                int        `json:"retain"`
}

// HistoryExportScheduler materializes the durable alert-transition history to
// local files on a fixed interval (Phase 34). It reuses the store's ReadAll and
// the Phase 33 pure serializers so the on-disk snapshots are byte-identical in
// shape to the on-demand HTTP export. Publication is atomic and no-replace:
// a crash mid-write never leaves a half-written formal file, and an existing
// snapshot is never overwritten (P34-I3).
type HistoryExportScheduler struct {
	cfg    HistoryExportConfig
	clock  func() time.Time
	logger *slog.Logger

	// Phase 37: signer is nil unless a signing key is configured (in which case
	// manifests are written as schema v3 and signed); trust is the verification
	// trust anchor, loaded from an independent configuration source.
	signer *exportSigner
	trust  *exportTrustStore

	mu             sync.Mutex
	started        bool
	running        bool // a tick is currently active (non-reentrant guard, P34-I1)
	lastRunAt      time.Time
	lastExportedAt time.Time
	lastError      string
	pruneError     string
	manifestError  string
	pubStateError  string
	signatureError string
	ledgerError    string
	skipCount      int64
	published      int64
	failed         int64

	done chan struct{}

	// beforeManifestPublish is a TEST-ONLY hook invoked at the start of
	// publishManifest — i.e. inside the manifest reservation window. It lets
	// tests probe the reservation lifecycle (T43). Production leaves it nil.
	beforeManifestPublish func(dir, manifestName string)

	// beforeManifestSign is a TEST-ONLY hook invoked after the manifest is
	// fully built and just before it is signed. It lets tests force a signing
	// failure and prove the fail-closed path (T68). Production leaves it nil.
	beforeManifestSign func(dir string)
}

// NewHistoryExportScheduler validates the config and builds a scheduler
// (P34-I5 fail-fast on illegal config). It does NOT start ticking; call
// Start(parent) to begin.
func NewHistoryExportScheduler(cfg HistoryExportConfig) (*HistoryExportScheduler, error) {
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("history export: interval must be > 0")
	}
	if cfg.Dir == "" {
		return nil, fmt.Errorf("history export: dir must be set")
	}
	if len(cfg.Formats) == 0 {
		return nil, fmt.Errorf("history export: at least one format required")
	}
	for _, f := range cfg.Formats {
		switch f {
		case "json", "csv":
		default:
			return nil, fmt.Errorf("history export: unsupported format %q (use json|csv)", f)
		}
	}
	if cfg.Retain < 0 {
		return nil, fmt.Errorf("history export: retain must be >= 0 (0 = keep all)")
	}
	// R160 (durable-only): a nil Store is a fail-fast config error, never a
	// silent no-op scheduler. A non-nil but erroring store is handled at Tick
	// time (degraded-store skip), not here.
	if cfg.Store == nil {
		return nil, fmt.Errorf("history export: store is required (durable-only export)")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	// Phase 37: load the signer (if configured) and the independent trust
	// anchor. Both are fail-fast: an unreadable/invalid key, a duplicate trust
	// key id, or a configured key id that disagrees with the derived one aborts
	// construction rather than producing manifests nobody can verify.
	signer, err := newExportSigner(cfg.SignKeyPath, cfg.SignKeyID)
	if err != nil {
		return nil, fmt.Errorf("history export: %w", err)
	}
	trust, err := newExportTrustStore(cfg.TrustKeyPaths)
	if err != nil {
		return nil, fmt.Errorf("history export: %w", err)
	}
	if signer != nil {
		// Signing with a key our own verifier cannot resolve would publish a
		// batch of snapshots that are GUARANTEED to verify as key_unknown. Both
		// failure modes are therefore fail-fast at construction (T79b):
		//   - a signing key with NO trust anchor at all;
		//   - a signing key absent from the configured anchor.
		if trust == nil {
			return nil, fmt.Errorf("history export: sign key configured (%s) but no trust anchor — also configure --export-trust-keys so published snapshots can actually be verified", signer.keyID)
		}
		if _, ok := trust.keys[signer.keyID]; !ok {
			return nil, fmt.Errorf("history export: sign key %s is absent from the configured trust keys — signed snapshots would verify as key_unknown", signer.keyID)
		}
	}
	// Phase 35 provisioning: a FRESH export directory (no manifests) gets its
	// publication watermark initialized to 0. This is provisioning, never
	// recovery — a runtime loss/corruption of the state file surfaces as the
	// fail-closed DEGRADED skip inside allocatePublicationID (M7), never a
	// max-scan guess (R175 v8).
	ensurePublicationState(cfg.Dir)
	return &HistoryExportScheduler{
		cfg:    cfg,
		clock:  clock,
		logger: cfg.Logger,
		signer: signer,
		trust:  trust,
	}, nil
}

// Start launches the ticker loop bound to parent's lifecycle. It is
// fire-and-forget (returns immediately) and idempotent: a second Start on the
// same instance is a no-op (P34-I4-ARCH-1). Shutdown completion is observable
// via Done().
func (s *HistoryExportScheduler) Start(parent context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.done = make(chan struct{})
	s.mu.Unlock()

	go s.run(parent)
}

func (s *HistoryExportScheduler) run(parent context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
			s.Tick(parent)
		}
	}
}

// Tick performs one export attempt synchronously. It is the unit of work the
// ticker calls; tests also call it directly for deterministic assertions. A
// Tick already in flight rejects a concurrent Tick (skip, not parallel) — the
// single-active invariant (P34-I1).
func (s *HistoryExportScheduler) Tick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.skipCount++
		s.mu.Unlock()
		return
	}
	s.running = true
	s.lastRunAt = s.clock()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	// Store is guaranteed non-nil by New (R160 fail-fast). A configured store
	// that errors/corrupts at runtime is a degraded-store SKIP further below.
	res := s.cfg.Store.ReadAll(ctx)
	if res.LoadErr != nil {
		s.mu.Lock()
		s.lastError = "read error: " + res.LoadErr.Error()
		// Published/Failed are LAST-TICK format counts (R160/R162): a skipped
		// tick attempted no formats, so both reset to zero.
		s.published, s.failed = 0, 0
		s.manifestError, s.pubStateError = "", ""
		s.ledgerError = ""
		s.mu.Unlock()
		return
	}
	if res.Corrupt {
		s.mu.Lock()
		s.lastError = "durable history corrupt — skipped"
		s.published, s.failed = 0, 0
		s.manifestError, s.pubStateError = "", ""
		s.ledgerError = ""
		s.mu.Unlock()
		return
	}

	// Publish ALL formats of this tick under ONE shared snapshot base identity
	// (R162/B). Collision retries advance one ordinal for the whole group;
	// per-format write failures stay independent and are reported explicitly,
	// never masked as a full success (P34-I2). The Phase 35 manifest is
	// published only after EVERY format succeeded (M1).
	pubErrs, manifestErr, pubStateErr := s.publishSnapshot(res)

	s.mu.Lock()
	if pubStateErr != "" {
		// M7 fail-closed: the watermark could not prove never-reuse, so the
		// tick published NOTHING. The skip is surfaced explicitly and the
		// per-format counters stay at zero (no format was attempted).
		s.lastError = pubStateErr
		s.published, s.failed = 0, 0
		s.manifestError, s.pubStateError = "", pubStateErr
		s.mu.Unlock()
		if perr := s.prune(); perr != nil {
			s.mu.Lock()
			s.pruneError = perr.Error()
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.pruneError = ""
			s.mu.Unlock()
		}
		return
	}
	switch {
	case len(pubErrs) == 0:
		// Full success: record provenance, clear any prior error.
		s.lastExportedAt = res.ExportedAt
		s.lastError = ""
	case len(pubErrs) < len(s.cfg.Formats):
		// Partial success: provenance recorded; explicit partial error.
		s.lastExportedAt = res.ExportedAt
		s.lastError = "partial export: " + strings.Join(pubErrs, "; ")
	default:
		// Total failure: no file was published.
		s.lastError = "export failed: " + strings.Join(pubErrs, "; ")
	}
	// Published/Failed are the LAST tick's per-format counts (R160), NOT
	// cumulative counters (R162/B fix).
	s.published = int64(len(s.cfg.Formats) - len(pubErrs))
	s.failed = int64(len(pubErrs))
	s.manifestError = manifestErr
	s.pubStateError = pubStateErr
	s.mu.Unlock()

	// Prune is independent of publish success (P34-STATE-1): its error is
	// reported separately and never overwrites lastError/lastExportedAt.
	if err := s.prune(); err != nil {
		s.mu.Lock()
		s.pruneError = err.Error()
		s.mu.Unlock()
	} else {
		s.mu.Lock()
		s.pruneError = ""
		s.mu.Unlock()
	}
}

// publishSnapshot atomically materializes ALL formats of one tick — plus its
// Phase 35 manifest — under a single shared snapshot base identity (P34-I3,
// R160/R162, R176 Architecture). Scheme per ordinal:
//  1. reserve every format's slot AND the manifest slot via sentinel files
//     (O_CREATE|O_EXCL) — one shared base = alert-transitions-<ts>[.<ordinal>].
//     Any EEXIST during reservation means THIS base ordinal is occupied, so the
//     WHOLE group advances (no-replace preserved; never per-format retry
//     suffixes — R162/B);
//  2. write each format's .tmp (Sync before link). Write failures are
//     PER-FORMAT errors and do NOT move the ordinal (P34-I2 partial export);
//  3. pre-check every target with Lstat BEFORE linking: if a legacy/external file
//     already occupies ANY target the group advances without linking at all, so
//     the common collision case never deletes anything. os.Link is still
//     no-replace, so a residual race-EEXIST rolls the group forward — and that
//     rollback is OWNERSHIP-SAFE (removeOwnArtifact proves the path is still the
//     file we published; a foreign replacement is reported, never deleted);
//  4. M1 (Phase 35): ONLY when every format published successfully, allocate
//     the durable publication_id (fail-closed M7), hash the formal artifacts,
//     and publish the manifest through its reserved slot (tmp → Sync → Link
//     no-replace). A partial export publishes NO manifest ⇒ its artifacts are
//     reported as orphan_artifact by the verify surface (M5).
//
// A crash between steps 2 and 3 leaves only .tmp + sentinel debris; never a
// corrupt formal file, and never a silently-overwritten one.
// Returns (per-format error strings, manifest publish error, watermark error).
func (s *HistoryExportScheduler) publishSnapshot(res protection.TransitionReadResult) (pubErrs []string, manifestErr string, pubStateErr string) {
	// Filesystem-safe timestamp: RFC3339Nano has ':' which is Windows-invalid,
	// so use a colon-free layout. res.ExportedAt is store provenance (P34-CLOCK-1).
	safe := res.ExportedAt.UTC().Format("20060102T150405.999999999") + "Z"
	base := fmt.Sprintf("%s-%s", historyExportFileBase, safe)

	for ordinal := 0; ordinal < 100; ordinal++ {
		names := make(map[string]string, len(s.cfg.Formats)+1)
		for _, f := range s.cfg.Formats {
			name := base
			if ordinal > 0 {
				name = fmt.Sprintf("%s.%d", base, ordinal)
			}
			names[f] = name + "." + f
		}
		names["manifest"] = base
		if ordinal > 0 {
			names["manifest"] = fmt.Sprintf("%s.%d", base, ordinal)
		}
		names["manifest"] += ".manifest.json"

		// 1. Reserve the WHOLE group's slots — formats AND manifest (EEXIST =>
		// ordinal occupied). Every sentinel WE create is recorded so cleanup can
		// later prove ownership instead of removing pathnames (R164/B).
		sentinelInfo := make(map[string]os.FileInfo, len(s.cfg.Formats)+1)
		tmpInfo := make(map[string]os.FileInfo, len(s.cfg.Formats)+1)
		reserveErr := ""
		reserveExist := false
		reserve := func(key string) {
			if reserveErr != "" {
				return
			}
			sf, err := os.OpenFile(filepath.Join(s.cfg.Dir, names[key]+".reserve"), os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				reserveErr = key + ": reserve slot: " + err.Error()
				reserveExist = os.IsExist(err)
				return
			}
			if si, serr := sf.Stat(); serr == nil {
				sentinelInfo[key] = si
			}
			sf.Close()
		}
		for _, f := range s.cfg.Formats {
			reserve(f)
		}
		reserve("manifest")
		if reserveErr != "" {
			// Clean up ONLY the sentinels this scheduler created (ownership-safe).
			s.cleanupArtifacts(names, nil, sentinelInfo)
			if reserveExist {
				continue // base ordinal occupied -> the whole group advances
			}
			return []string{reserveErr}, "", ""
		}

		// 2. Per-format tmp writes (write failures never move the ordinal).
		writeErrs := make(map[string]error, len(s.cfg.Formats))
		for _, f := range s.cfg.Formats {
			info, werr := s.writeTmp(filepath.Join(s.cfg.Dir, names[f]+".tmp"), f, res)
			if werr != nil {
				writeErrs[f] = werr
				continue
			}
			tmpInfo[f] = info // we created it -> we own it
		}

		// 2b. Pre-check EVERY target BEFORE any link: if an external/legacy file
		// already occupies ANY format's target, the group advances immediately and
		// no link is performed at all — so the common collision case never
		// publishes and therefore never has to delete anything (R163/B).
		occupied := false
		linkCollide := false
		linkErrs := make(map[string]error)
		for _, f := range s.cfg.Formats {
			if writeErrs[f] != nil {
				continue
			}
			if _, err := os.Lstat(filepath.Join(s.cfg.Dir, names[f])); err == nil {
				occupied = true
				break
			} else if !os.IsNotExist(err) {
				linkErrs[f] = err // path unusable: report it, never guess
			}
		}

		// 3. No-replace links. After the pre-check, an EEXIST here can only be a
		// RACE (someone created the target between the check and the link).
		linked := make(map[string]os.FileInfo)
		if !occupied {
			for _, f := range s.cfg.Formats {
				if writeErrs[f] != nil || linkErrs[f] != nil {
					continue
				}
				target := filepath.Join(s.cfg.Dir, names[f])
				if lerr := os.Link(target+".tmp", target); lerr != nil {
					if os.IsExist(lerr) {
						linkCollide = true
						break
					}
					linkErrs[f] = lerr
					continue
				}
				// Ownership evidence: remember the EXACT file we published
				// (volume + file index), not merely its pathname, so a later
				// rollback can prove the path still IS our artifact.
				if info, serr := os.Lstat(target); serr == nil {
					linked[f] = info
				}
			}
		}

		// Phase 35 (R178/B): the manifest sentinel's reservation MUST stay held
		// until the manifest itself is published (R176 A4 lifecycle). Cleanup is
		// therefore split: artifact debris is cleaned per-path below, while the
		// "manifest" key is retained until after publishManifest returns.
		artifactTmp := make(map[string]os.FileInfo, len(tmpInfo))
		artifactSent := make(map[string]os.FileInfo, len(sentinelInfo))
		for k, v := range tmpInfo {
			if k != "manifest" {
				artifactTmp[k] = v
			}
		}
		for k, v := range sentinelInfo {
			if k != "manifest" {
				artifactSent[k] = v
			}
		}
		if occupied {
			// Nothing was linked at this ordinal: no manifest will be published
			// here either, so release the whole group's slots and advance.
			// Never fall through to the success path here — that would report
			// success for a tick that published no file (P34-I2).
			s.cleanupArtifacts(names, tmpInfo, sentinelInfo)
			continue
		}
		if linkCollide {
			// Roll back ONLY artifacts that are provably still OURS — never a
			// pathname that may have been replaced by another process (R163/B).
			for f, info := range linked {
				target := filepath.Join(s.cfg.Dir, names[f])
				if rerr := removeOwnArtifact(target, info); rerr != nil {
					linkErrs[f] = rerr
				}
			}
			s.cleanupArtifacts(names, tmpInfo, sentinelInfo)
			continue // the whole group retries at the next ordinal
		}

		var errs []string
		for _, f := range s.cfg.Formats {
			switch {
			case writeErrs[f] != nil:
				errs = append(errs, f+": write tmp: "+writeErrs[f].Error())
			case linkErrs[f] != nil:
				errs = append(errs, f+": link snapshot: "+linkErrs[f].Error())
			}
		}
		if len(errs) > 0 {
			// M1: a partial export publishes NO manifest — the verify surface
			// reports these artifacts as orphan_artifact (explicit, M5). The
			// manifest slot is no longer needed: release it.
			s.cleanupArtifacts(names, tmpInfo, sentinelInfo)
			return errs, "", ""
		}

		// 4. Phase 35 manifest (R176 A4 order, restored per R179/B):
		//    all artifact Links succeeded → hash formal artifacts →
		//    allocatePublicationID → build/write/link manifest. A failed
		//    attempt therefore consumes NO publication id; a crash after id
		//    persistence still only creates a legal gap (M7). Artifact debris
		//    is released first; the manifest sentinel is HELD across the
		//    hash/allocate/tmp/link window (R178/B — reservation lifecycle).
		s.cleanupArtifacts(names, artifactTmp, artifactSent)
		mErr, sErr := s.publishManifest(res, base, ordinal, names, s.clock().UTC())
		// The manifest publication window is complete: release its slot.
		s.cleanupArtifacts(names, nil, map[string]os.FileInfo{"manifest": sentinelInfo["manifest"]})
		return nil, mErr, sErr
	}
	return []string{"could not reserve a free snapshot slot after 100 retries"}, "", ""
}

// publishManifest allocates the durable publication_id, hashes the formal
// artifacts, and publishes the manifest through its reserved slot (atomic
// no-replace, M1/M2). The manifest name has ALREADY been reserved with the
// artifact group at this ordinal.
func (s *HistoryExportScheduler) publishManifest(res protection.TransitionReadResult, base string, ordinal int, names map[string]string, generatedAt time.Time) (manifestErr string, pubStateErr string) {
	// TEST-ONLY hook: we are now INSIDE the manifest reservation window —
	// the sentinel is still held and the formal target is not yet linked.
	if s.beforeManifestPublish != nil {
		s.beforeManifestPublish(s.cfg.Dir, names["manifest"])
	}
	formats := make([]manifestFormatInfo, 0, len(s.cfg.Formats))
	for _, f := range s.cfg.Formats {
		sum, size, herr := hashFile(filepath.Join(s.cfg.Dir, names[f]))
		if herr != nil {
			return "manifest: hash " + f + ": " + herr.Error(), ""
		}
		formats = append(formats, manifestFormatInfo{
			Format:  f,
			File:    names[f],
			SHA256:  sum,
			Bytes:   size,
			Records: int64(len(res.Transitions)),
		})
	}
	// R176 A4 / R179/B: the durable publication_id is allocated ONLY after
	// every format published successfully and the artifacts were hashed —
	// failed attempts never consume an id (T44), while a crash after the id
	// is persisted only creates a legal gap (M7 never-reuse still holds).
	pubID, err := allocatePublicationID(s.cfg.Dir)
	if err != nil {
		// Fail-closed M7: without a provable watermark there is no manifest.
		// The already-linked artifacts stay on disk and are honestly reported
		// as orphan_artifact by the verify surface (M5).
		return "", err.Error()
	}
	hs := historyExportStats(res)
	identity := strings.TrimPrefix(base, historyExportFileBase+"-")
	if ordinal > 0 {
		identity = fmt.Sprintf("%s.%d", identity, ordinal)
	}
	manifest := snapshotManifest{
		SchemaVersion: manifestSchemaVersion,
		PublicationID: pubID,
		Snapshot:      identity,
		ExportedAt:    res.ExportedAt.UTC().Format(time.RFC3339Nano),
		GeneratedAt:   generatedAt.Format(time.RFC3339Nano),
		Source:        "durable",
		Truncated:     hs.Truncated,
		SeqContinuity: seqContinuityValue,
		MinSeq:        res.MinSeq,
		MaxSeq:        res.MaxSeq,
		Records:       int64(len(res.Transitions)),
		Formats:       formats,
	}
	// ---- Phase 37: fail-closed signing (A4) --------------------------------
	// Signing happens ONLY when a key is configured, and only after the manifest
	// content is final. Without a key the output stays byte-for-byte the Phase 36
	// v2 document (default behaviour unchanged); with a key the manifest becomes
	// v3 + signature, and a signing failure publishes NOTHING (the already
	// linked artifacts are honestly reported as orphan_artifact).
	if s.signer != nil {
		manifest.SchemaVersion = manifestSchemaVersionV4
		// Phase 38: commit to the manifest this publication ACTUALLY follows —
		// never `id-1`, because a crashed tick burns ids and leaves a legal gap.
		// If the newest chain-bearing manifest exists but cannot be trusted, the
		// publication is REFUSED rather than silently skipping back to an older
		// node (R198: skipping would disguise an already-broken chain).
		prev, lerr := latestVerifiedChainPredecessor(s.cfg.Dir, pubID, s.trust)
		if lerr != nil {
			return "manifest: chain: " + lerr.Error(), ""
		}
		if prev == nil {
			// Genesis: the first chain-bearing manifest declares the P38 chain
			// start EXPLICITLY. It deliberately does not adopt an older v3 file
			// as its predecessor: such a file cannot express a chain, and once
			// pruned it is indistinguishable from a deleted first v4 link
			// (R195 migration boundary).
			manifest.Chain = &manifestChain{PrevPublicationID: 0}
		} else {
			// Phase 39 recovery: a manifest-first crash leaves a published
			// manifest whose ledger entry was never written. Backfill it now.
			// The predecessor passed the P37 signature check inside
			// latestVerifiedChainPredecessor, and recordLedgerEntry re-checks it,
			// so a tampered manifest can never be washed into valid ledger
			// evidence (R204).
			if rerr := s.recordLedgerEntry(prev, generatedAt); rerr != nil {
				s.setLedgerError(rerr.Error())
			}
			dg, derr := manifestDigest(prev)
			if derr != nil {
				return "manifest: chain digest: " + derr.Error(), ""
			}
			manifest.Chain = &manifestChain{PrevPublicationID: prev.PublicationID, PrevManifestDigest: dg}
		}
		if s.beforeManifestSign != nil {
			s.beforeManifestSign(s.cfg.Dir)
		}
		if serr := s.signer.signManifest(&manifest, generatedAt); serr != nil {
			return "manifest: sign: " + serr.Error(), ""
		}
	}
	tmpPath := filepath.Join(s.cfg.Dir, names["manifest"]+".tmp")
	target := filepath.Join(s.cfg.Dir, names["manifest"])
	mf, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "manifest: write tmp: " + err.Error(), ""
	}
	werr := serializeSnapshotManifest(mf, &manifest)
	var syncErr error
	if werr == nil {
		syncErr = mf.Sync()
	}
	if cerr := mf.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = syncErr
	}
	if werr != nil {
		os.Remove(tmpPath)
		return "manifest: write tmp: " + werr.Error(), ""
	}
	if lerr := os.Link(tmpPath, target); lerr != nil {
		os.Remove(tmpPath)
		if os.IsExist(lerr) {
			// Race: a foreign file appeared at the reserved manifest slot. Never
			// overwrite; the artifacts are honestly reported as orphan_artifact.
			return "manifest: link target occupied: " + names["manifest"], ""
		}
		return "manifest: link: " + lerr.Error(), ""
	}
	os.Remove(tmpPath)
	// Phase 39: the ledger entry is appended ONLY after the manifest is durably
	// published. This is the single success point — ledger-first would create
	// phantom publication evidence, so it is never attempted. A ledger failure
	// never un-publishes the manifest; it is surfaced as ledger_error and the
	// next tick's recovery backfills it.
	if s.signer != nil {
		if rerr := s.recordLedgerEntry(&manifest, generatedAt); rerr != nil {
			s.setLedgerError(rerr.Error())
		}
	}
	return "", ""
}

// recordLedgerEntry appends the signed ledger entry describing a published
// manifest. It is IDEMPOTENT (same id + same digest is a no-op; a different
// digest is refused as a conflict) and only ever signs a manifest whose P37
// signature verifies — a tampered manifest must never be washed into valid
// ledger evidence.
func (s *HistoryExportScheduler) recordLedgerEntry(m *snapshotManifest, at time.Time) error {
	if s.signer == nil || m == nil {
		return nil
	}
	if v := verifyManifestSignature(m, s.trust); v.Verdict != sigVerdictOK {
		return fmt.Errorf("ledger: refusing to record publication %d (%s)", m.PublicationID, v.Verdict)
	}
	e, err := entryForManifest(m, at)
	if err != nil {
		return err
	}
	if err := s.signer.signLedgerEntry(&e, at); err != nil {
		return err
	}
	return appendLedgerEntry(s.cfg.Dir, s.cfg.LedgerCapacity, e)
}

func (s *HistoryExportScheduler) setLedgerError(msg string) {
	s.mu.Lock()
	s.ledgerError = msg
	s.mu.Unlock()
}

// writeTmp streams one format into tmpPath and returns the FileInfo of the file
// it created, so the caller can later prove OWNERSHIP before deleting it
// (R164/B — cleanup must never remove a pathname blindly). O_EXCL is used: the
// sentinel already reserves this slot, so an existing tmp path here is not ours
// and is refused instead of being truncated (no foreign data is ever destroyed).
// On success the caller owns the tmp; on error no ownership is granted.
func (s *HistoryExportScheduler) writeTmp(tmpPath, fmtName string, res protection.TransitionReadResult) (info os.FileInfo, err error) {
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Stat while the fd is still open: this is the file WE created.
		if fi, serr := f.Stat(); serr == nil {
			info = fi
		}
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
	}()
	// Stream straight into the file (no full-buffer copy); the serializer
	// writers are the same pure functions the HTTP export uses.
	if werr := serializeHistoryExportForFormat(f, fmtName, res); werr != nil {
		return nil, werr
	}
	return nil, f.Sync()
}

// cleanupArtifacts removes the .tmp and .reserve debris THIS scheduler created
// at one ordinal — and only that debris. Keys are format names plus the special
// "manifest" key (Phase 35). Each candidate is identified by the os.FileInfo
// captured when we created it (nil = we never created it, so it is left alone),
// and removeOwnArtifact re-verifies identity before unlinking. A pathname that
// has since been replaced by another process is therefore preserved; cleanup
// errors are best-effort and never affect the publish result (P34-STATE: they
// are not format failures).
func (s *HistoryExportScheduler) cleanupArtifacts(names map[string]string, tmpInfo, sentinelInfo map[string]os.FileInfo) {
	for key, info := range tmpInfo {
		if info != nil {
			removeOwnArtifact(filepath.Join(s.cfg.Dir, names[key]+".tmp"), info)
		}
	}
	for key, info := range sentinelInfo {
		if info != nil {
			removeOwnArtifact(filepath.Join(s.cfg.Dir, names[key]+".reserve"), info)
		}
	}
}

// removeOwnArtifact removes path ONLY IF it is still the exact file identified
// by want — the os.FileInfo captured when WE published it. os.SameFile compares
// the volume serial and file index, so a pathname that has since been replaced
// (deleted and recreated) by another process is recognised as foreign and is
// NEVER deleted; the caller gets an error instead and reports it (R163/B:
// rollback must be ownership-safe, not pathname-based). A path that is already
// gone is treated as success (there is nothing of ours left to remove).
func removeOwnArtifact(path string, want os.FileInfo) error {
	cur, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !os.SameFile(want, cur) {
		return fmt.Errorf("refusing to remove %s: no longer the artifact we published", path)
	}
	return os.Remove(path)
}

// serializeHistoryExportForFormat dispatches to the shared Phase 33 serializers
// (JSON/CSV). Centralized so the on-disk snapshot matches the HTTP export shape.
func serializeHistoryExportForFormat(w io.Writer, fmtName string, res protection.TransitionReadResult) error {
	switch fmtName {
	case "json":
		return serializeHistoryExportJSON(w, res)
	case "csv":
		return serializeHistoryExportCSV(w, res)
	default:
		return fmt.Errorf("unsupported format %q", fmtName)
	}
}

// prune enforces the bounded local retention cap (P34-I6) in SNAPSHOT units,
// driven EXCLUSIVELY by the shared canonical resolver (R172/R176 — verify and
// prune must never drift). 0 keeps all snapshots. For every non-ignored file
// the retention unit is snapshotGroupKeyFromIdentity(canonical owner), where
// the canonical owner is the file's own valid identity or its nearest valid
// ancestor ("…Z.extra" → "…Z"). Consequences (all intentional, T36/T39):
//   - one export's .json/.csv/.manifest.json share one unit (M4);
//   - collision variants "…Z" / "…Z.1" share the same ExportedAt unit (R162);
//   - undeclared extras owned by a manifest group are removed WITH that group,
//     so no orphan artifact ever survives its owner (M6/A6.3);
//   - unattributable files (no valid-identity prefix at all) are NEVER deleted
//     by prune — fail-closed retention, surfaced by verify as unknown instead.
//
// Oldest excess units are removed together.
func (s *HistoryExportScheduler) prune() error {
	if s.cfg.Retain <= 0 {
		return nil // 0 = keep all
	}
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		return fmt.Errorf("prune read dir: %w", err)
	}
	units := make(map[string][]string) // retention unit (ts) -> file names
	var order []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		self, kind := parseSnapshotFile(e.Name())
		if kind == snapshotKindIgnore || self == "" {
			continue
		}
		owner := self
		if !snapshotIdentityValid(self) {
			owner = nearestValidAncestor(self)
			if owner == "" {
				continue // unattributable: prune never deletes what it cannot own
			}
		}
		unit := snapshotGroupKeyFromIdentity(owner)
		if _, ok := units[unit]; !ok {
			order = append(order, unit)
		}
		units[unit] = append(units[unit], e.Name())
	}
	if len(units) <= s.cfg.Retain {
		return nil
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	excess := len(units) - s.cfg.Retain
	for i := 0; i < excess; i++ {
		for _, fn := range units[order[i]] {
			if err := os.Remove(filepath.Join(s.cfg.Dir, fn)); err != nil {
				return fmt.Errorf("prune remove %s: %w", fn, err)
			}
		}
	}
	return nil
}

// snapshotGroupKey returns the retention unit (ExportedAt) for an export-dir
// file, via the shared canonical resolver (R172): parse → canonical owner →
// strip the collision ordinal. Test-facing helper; prune uses the same path.
func snapshotGroupKey(name string) string {
	self, kind := parseSnapshotFile(name)
	if kind == snapshotKindIgnore || self == "" {
		return name
	}
	if !snapshotIdentityValid(self) {
		self = nearestValidAncestor(self)
		if self == "" {
			return name
		}
	}
	return snapshotGroupKeyFromIdentity(self)
}

// Status returns a snapshot of the scheduler state for the read API.
func (s *HistoryExportScheduler) Status() HistoryExportStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := HistoryExportStatus{
		Enabled:               true,
		Running:               s.running,
		LastError:             s.lastError,
		PruneError:            s.pruneError,
		ManifestError:         s.manifestError,
		PublicationStateError: s.pubStateError,
		SignatureError:        s.signatureError,
		LedgerError:           s.ledgerError,
		SkipCount:             s.skipCount,
		Published:             s.published,
		Failed:                s.failed,
		Dir:                   s.cfg.Dir,
		Interval:              s.cfg.Interval.String(),
		Formats:               s.cfg.Formats,
		Retain:                s.cfg.Retain,
		SigningEnabled:        s.signer != nil,
	}
	if s.trust != nil {
		st.TrustedKeys = len(s.trust.keys)
	}
	if s.signer != nil {
		st.SignerKeyID = s.signer.keyID
	}
	// Phase 37: surface a signing failure distinctly (it is the fail-closed
	// path where the manifest was deliberately NOT published).
	if strings.HasPrefix(s.manifestError, "manifest: sign:") {
		st.SignatureError = s.manifestError
	}
	// Phase 38: a refused chain extension is its own fail-closed state.
	if strings.HasPrefix(s.manifestError, "manifest: chain:") {
		st.ChainError = s.manifestError
	}
	if !s.lastRunAt.IsZero() {
		t := s.lastRunAt
		st.LastRunAt = &t
	}
	if !s.lastExportedAt.IsZero() {
		t := s.lastExportedAt
		st.LastExportedAt = &t
	}
	return st
}

// Done returns a channel closed when the run loop has exited (parent cancelled
// and in-flight tick settled). Tests must wait on this — NOT on Start() (which
// is fire-and-forget, P34-I4-ARCH-2).
func (s *HistoryExportScheduler) Done() <-chan struct{} {
	return s.done
}
