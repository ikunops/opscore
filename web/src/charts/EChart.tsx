import { useEffect, useRef } from 'react'
import * as echarts from 'echarts'
import 'echarts-liquidfill'

// EChart 是 ECharts 的 React 封装:挂载时初始化,option 变化时增量更新,自适应尺寸。
export default function EChart({
  option,
  height = 260,
}: {
  option: any
  height?: number | string
}) {
  const ref = useRef<HTMLDivElement>(null)
  const chart = useRef<echarts.ECharts | null>(null)

  useEffect(() => {
    if (!ref.current) return
    chart.current = echarts.init(ref.current)
    const onResize = () => chart.current?.resize()
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
      chart.current?.dispose()
      chart.current = null
    }
  }, [])

  useEffect(() => {
    chart.current?.setOption(option, true)
  }, [option])

  return <div ref={ref} style={{ width: '100%', height }} />
}
