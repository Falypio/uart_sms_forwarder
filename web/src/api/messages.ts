import apiClient from './client';
import {devicePath} from './devices';
import type {Stats, Conversation, TextMessage} from './types';

export const getStats = (id: string, signal?: AbortSignal) => apiClient.get<Stats>(devicePath(id, '/messages/stats'), {signal});
export const getConversations = (id: string, signal?: AbortSignal) => apiClient.get<Conversation[]>(devicePath(id, '/messages/conversations'), {signal});
export const getConversationMessages = (id: string, peer: string, signal?: AbortSignal) => apiClient.get<TextMessage[]>(devicePath(id, `/messages/conversations/${encodeURIComponent(peer)}/messages`), {signal});
export const deleteMessage = (id: string, messageId: string) => apiClient.delete<void>(devicePath(id, `/messages/${encodeURIComponent(messageId)}`));
export const deleteConversation = (id: string, peer: string) => apiClient.delete<void>(devicePath(id, `/messages/conversations/${encodeURIComponent(peer)}`));
export const clearMessages = (id: string) => apiClient.delete<void>(devicePath(id, '/messages'));