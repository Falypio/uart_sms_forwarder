import {createContext, useContext} from 'react';
import type {DeviceRecord} from '@/api/devices';

export interface DeviceContextValue {
    deviceId: string;
    device: DeviceRecord;
    isActive: () => boolean;
}
export const DeviceContext = createContext<DeviceContextValue | null>(null);
export const useOptionalDevice = () => useContext(DeviceContext);
export function useDevice() {
    const value = useOptionalDevice();
    if (!value) throw new Error('请先选择设备');
    return value;
}
