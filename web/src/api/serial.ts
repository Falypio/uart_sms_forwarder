import apiClient from './client';
import {devicePath} from './devices';
import type {DeviceCommand} from './devices';
import type {DeviceStatus, MobileInfo, SendSMSRequest} from './types';

export const sendSMS = (id: string, data: SendSMSRequest) => apiClient.post<DeviceCommand>(devicePath(id, '/serial/sms'), data);
export const setFlymode = (id: string, enabled: boolean, durationSeconds?: number) => apiClient.post<DeviceCommand>(devicePath(id, '/serial/flymode'), {enabled, ...(durationSeconds !== undefined && {duration_seconds: durationSeconds})});
export const rebootMcu = (id: string) => apiClient.post<DeviceCommand>(devicePath(id, '/serial/reboot'));
export const resetStack = (id: string) => apiClient.post<DeviceCommand>(devicePath(id, '/serial/reset-stack'));

export const getStatus = async (id: string, signal?: AbortSignal): Promise<DeviceStatus> => {
    const raw = await apiClient.get<Partial<DeviceStatus>>(devicePath(id, '/status'), {signal});
    const emptyMobile: MobileInfo = {sim_ready: false, iccid: '', imsi: '', rssi: 0, signal_level: 0, signal_desc: '', is_registered: false, is_roaming: false, operator: '', csq: 0, rsrp: 0, rsrq: 0, number: '', uptime: 0};
    return {
        type: raw.type || 'status_response', timestamp: raw.timestamp || 0,
        mem_kb: raw.mem_kb ?? raw.memory_usage ?? 0, flymode: raw.flymode ?? raw.mobile?.flymode ?? false,
        mobile: {...emptyMobile, ...raw.mobile}, port_name: raw.port_name || '',
        locating: raw.locating, connected: Boolean(raw.connected), version: raw.version || '', deviceId: id, accessMode: raw.accessMode,
    };
};