import {devicePath} from './devices';
import type {DeviceCommand} from './devices';
// 定时任务配置
import apiClient from "@/api/client.ts";

export type LastRunStatus = 'unknown' | 'success' | 'failed';

export interface ScheduledTask {
    id: string;
    name: string;
    enabled: boolean;
    intervalDays: number;
    phoneNumber: string;
    content: string;
    createdAt?: number;
    lastRunAt?: number;
    lastMsgId?: string;
    lastRunStatus?: LastRunStatus;
}

// 定时任务 API (RESTful)
// 获取所有定时任务
export const getScheduledTasks = (deviceId: string, signal?: AbortSignal) => {
    return apiClient.get<ScheduledTask[]>(devicePath(deviceId, '/scheduled-tasks'), {signal});
};

// 获取单个定时任务
export const getScheduledTask = (deviceId: string, id: string, signal?: AbortSignal) => {
    return apiClient.get<ScheduledTask>(devicePath(deviceId, `/scheduled-tasks/${encodeURIComponent(id)}`), {signal});
};

// 创建定时任务
export const createScheduledTask = (deviceId: string, task: Omit<ScheduledTask, 'id' | 'createdAt' | 'lastRunAt'>) => {
    return apiClient.post<ScheduledTask>(devicePath(deviceId, '/scheduled-tasks'), task);
};

// 更新定时任务
export const updateScheduledTask = (deviceId: string, id: string, task: Omit<ScheduledTask, 'id' | 'createdAt' | 'lastRunAt'>) => {
    return apiClient.put<ScheduledTask>(devicePath(deviceId, `/scheduled-tasks/${encodeURIComponent(id)}`), task);
};

// 删除定时任务
export const deleteScheduledTask = (deviceId: string, id: string) => {
    return apiClient.delete<void>(devicePath(deviceId, `/scheduled-tasks/${encodeURIComponent(id)}`));
};

// 立即触发定时任务
export const triggerScheduledTask = (deviceId: string, id: string) => {
    return apiClient.post<DeviceCommand>(devicePath(deviceId, `/scheduled-tasks/${encodeURIComponent(id)}/trigger`), {});
};