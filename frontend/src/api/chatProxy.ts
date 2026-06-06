/**
 * 网页端共享代理(ChatGPT 网页版)的"看门人"在线状态。
 * 后端透传 chatgate 的 /status:当前在用的代理账号 + 同时在线名额上限。
 * 未部署 / 不可达时 enabled=false(前端卡片自动隐藏)。
 */

import { apiClient } from './client'

export interface ChatProxyActiveUser {
  user: string
  idle_seconds: number
}

export interface ChatProxyStatus {
  enabled: boolean
  max: number
  active: ChatProxyActiveUser[]
}

/** 获取网页代理当前在线情况(全员可见)。 */
export async function getChatProxyStatus(): Promise<ChatProxyStatus> {
  const { data } = await apiClient.get<ChatProxyStatus>('/user/chat-proxy-status')
  return data
}

export default { getChatProxyStatus }
