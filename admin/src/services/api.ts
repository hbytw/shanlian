import axios, { AxiosError } from 'axios'
import type { ApiResponse, ErrorResponse } from '../types/api'

const api = axios.create({
  baseURL: '/api/v1',
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json',
  },
})

// 请求拦截器：添加 Token
api.interceptors.request.use(
  (config) => {
    const token = localStorage.getItem('access_token')
    if (token) {
      config.headers.Authorization = `Bearer ${token}`
    }
    return config
  },
  (error) => {
    return Promise.reject(error)
  }
)

// 响应拦截器：统一错误处理
api.interceptors.response.use(
  (response) => response,
  (error: AxiosError<ErrorResponse>) => {
    if (error.response) {
      const { message } = error.response.data?.error || {}
      console.error('API 错误：', message || error.message)
    } else if (error.request) {
      console.error('网络错误：请检查网络连接')
    }
    return Promise.reject(error)
  }
)

export default api

// 通用请求方法
export async function get<T>(url: string, params?: Record<string, unknown>): Promise<ApiResponse<T>> {
  const response = await api.get<ApiResponse<T>>(url, { params })
  return response.data
}

export async function post<T>(url: string, data?: unknown): Promise<ApiResponse<T>> {
  const response = await api.post<ApiResponse<T>>(url, data)
  return response.data
}

export async function put<T>(url: string, data?: unknown): Promise<ApiResponse<T>> {
  const response = await api.put<ApiResponse<T>>(url, data)
  return response.data
}

export async function patch<T>(url: string, data?: unknown): Promise<ApiResponse<T>> {
  const response = await api.patch<ApiResponse<T>>(url, data)
  return response.data
}

export async function del<T>(url: string): Promise<ApiResponse<T>> {
  const response = await api.delete<ApiResponse<T>>(url)
  return response.data
}
