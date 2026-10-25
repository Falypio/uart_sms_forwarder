import apiClient from './client';
import type {DeviceStatus, MobileInfo} from './types';

export interface DeviceRecord {
    id: string;
    name: string;
    accessMode: 'serial' | 'mqtt';
    online: boolean;
    port: string;
    lastSeen: number;
    createdAt: number;
    features: {
        locate_led?: boolean;
        locate_gpio?: number;
        device_id?: string;
        imei?: string;
        name?: string;
        model?: string;
        firmware?: string;
        version?: string;
        mobile?: Partial<MobileInfo>;
    } | null;
    status: Partial<DeviceStatus> | null;
}

export interface PublicKeyInfo {
    keyId: string;
    publicKey: string;
}

export interface Enrollment extends PublicKeyInfo {
    deviceId: string;
    registrationToken: string;
    topicPrefix: string;
}

export interface DeviceCommand {
    id: string;
    deviceId: string;
    action: string;
    status: 'pending' | 'accepted' | 'queued' | 'succeeded' | 'failed' | 'unknown';
    result: string;
    expiresAt: number;
    createdAt: number;
    updatedAt: number;
}

export const devicePath = (deviceId: string, path = '') => {
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(deviceId)) throw new Error('请先选择有效设备');
    return `/devices/${encodeURIComponent(deviceId)}${path}`;
};

export const deviceKey = (deviceId: string, ...parts: unknown[]) => ['device', deviceId, ...parts];
export const getDevices = (signal?: AbortSignal) => apiClient.get<DeviceRecord[]>('/devices', {signal});
export const getDevice = (id: string, signal?: AbortSignal) => apiClient.get<DeviceRecord>(devicePath(id), {signal});
export const renameDevice = (id: string, name: string) => apiClient.put<void>(devicePath(id), {name});
export const getPublicKey = (signal?: AbortSignal) => apiClient.get<PublicKeyInfo>('/devices/server-public-key', {signal});
export const enrollDevice = (deviceId: string) => apiClient.post<Enrollment>('/devices/enroll', {deviceId});
export const revokeDevice = (deviceId: string) => {
    devicePath(deviceId);
    return apiClient.post<void>(`/device-enrollments/${encodeURIComponent(deviceId)}/revoke`);
};
export const setLocate = (id: string, enabled: boolean) => apiClient.post<DeviceCommand>(devicePath(id, '/locate'), {enabled});
export const getCommand = (id: string, commandId: string, signal?: AbortSignal) => apiClient.get<DeviceCommand>(devicePath(id, `/commands/${encodeURIComponent(commandId)}`), {signal});
export const getCommands = (id: string, signal?: AbortSignal) => apiClient.get<DeviceCommand[]>(devicePath(id, '/commands'), {signal});

export function downloadText(filename: string, text: string) {
    const url = URL.createObjectURL(new Blob([text], {type: 'text/plain;charset=utf-8'}));
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
