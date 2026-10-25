import {devicePath} from './devices';
// ==================== 通用 Property 接口 ====================

// 通用的 Property 响应类型
import apiClient from "@/api/client.ts";

export interface PropertyResponse<T> {
    id: string;
    name: string;
    value: T;
}

// 通用的获取 Property 方法
export const getProperty = async <T>(deviceId: string, propertyId: string, signal?: AbortSignal): Promise<T> => {
    const response = await apiClient.get<PropertyResponse<T>>(devicePath(deviceId, `/properties/${encodeURIComponent(propertyId)}`), {signal});
    return response.value;
};

// 通用的保存 Property 方法
export const saveProperty = async <T>(deviceId: string, propertyId: string, name: string, value: T): Promise<void> => {
    await apiClient.put(devicePath(deviceId, `/properties/${encodeURIComponent(propertyId)}`), {
        name,
        value,
    });
};

// ==================== 通知渠道配置 ====================

const PROPERTY_ID_NOTIFICATION_CHANNELS = 'notification_channels';

// 通知渠道配置（通过 type 标识，不再使用独立ID）
export interface NotificationChannel {
    type: 'dingtalk' | 'wecom' | 'feishu' | 'email' | 'webhook' | 'telegram'; // 渠道类型，作为唯一标识
    enabled: boolean; // 是否启用
    config: Record<string, unknown>; // JSON配置，根据type不同而不同
}

// 获取通知渠道列表
export const getNotificationChannels = async (deviceId: string, signal?: AbortSignal): Promise<NotificationChannel[]> => {
    const channels = await getProperty<NotificationChannel[]>(deviceId, PROPERTY_ID_NOTIFICATION_CHANNELS, signal);
    return channels || [];
};

// 保存通知渠道列表
export const saveNotificationChannels = async (deviceId: string, channels: NotificationChannel[]): Promise<void> => {
    return saveProperty(deviceId, PROPERTY_ID_NOTIFICATION_CHANNELS, '通知渠道配置', channels);
};

// 测试通知渠道（从数据库读取配置）
export const testNotificationChannel = async (deviceId: string, type: string): Promise<void> => {
    return await apiClient.post<void>(devicePath(deviceId, `/notifications/${encodeURIComponent(type)}/test`));
};

// ==================== 自动飞行模式配置 ====================

const PROPERTY_ID_AUTO_FLYMODE_CONFIG = 'auto_flymode_config';

export interface AutoFlymodeConfig {
    enabled: boolean;
    idleTimeoutHours: number;
}

export const getAutoFlymodeConfig = async (deviceId: string, signal?: AbortSignal): Promise<AutoFlymodeConfig> => {
    const config = await getProperty<AutoFlymodeConfig>(deviceId, PROPERTY_ID_AUTO_FLYMODE_CONFIG, signal);
    return config || {
        enabled: false,
        idleTimeoutHours: 1,
    };
};

export const saveAutoFlymodeConfig = async (deviceId: string, config: AutoFlymodeConfig): Promise<void> => {
    return saveProperty(deviceId, PROPERTY_ID_AUTO_FLYMODE_CONFIG, '自动飞行模式配置', config);
};

export interface Version {
    version: string;
}

export const getVersion = () => {
    return apiClient.get<Version>('/version');
}
