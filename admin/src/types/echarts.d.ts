declare module 'echarts' {
  export interface ECharts {
    setOption(option: EChartsOption): void
    dispose(): void
    resize(): void
  }

  export interface EChartsOption {
    [key: string]: unknown
  }

  export const graphic: {
    LinearGradient: new (
      x0: number,
      y0: number,
      x1: number,
      y1: number,
      colorStops: { offset: number; color: string }[]
    ) => unknown
  }

  export function init(el: HTMLElement): ECharts
}
