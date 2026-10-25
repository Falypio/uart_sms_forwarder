import {useEffect, useRef, useState} from 'react';
import {useMutation, useQuery, useQueryClient} from '@tanstack/react-query';
import {Loader2, MapPin, Square} from 'lucide-react';
import {toast} from 'sonner';
import {deviceKey, getCommand, setLocate, type DeviceCommand} from '@/api/devices';
import {Button} from '@/components/ui/button';
import {cn} from '@/lib/utils';

interface Props {
    deviceId: string;
    name: string;
    online: boolean;
    supported: boolean;
    locating?: boolean;
    className?: string;
}

const finished = (status: DeviceCommand['status']) => ['succeeded', 'failed', 'unknown'].includes(status);

export default function LocateButton({deviceId, name, online, supported, locating, className}: Props) {
    const queryClient = useQueryClient();
    const [command, setCommand] = useState<DeviceCommand | null>(null);
    const mounted = useRef(false);
    const reported = useRef('');
    useEffect(() => {
        mounted.current = true;
        return () => {mounted.current = false;};
    }, []);
    const mutation = useMutation({
        mutationFn: (enabled: boolean) => setLocate(deviceId, enabled),
        onSuccess: (cmd) => {
            if (mounted.current) setCommand(cmd);
            void queryClient.invalidateQueries({queryKey: deviceKey(deviceId, 'commands')});
        },
        onError: (error) => {
            if (mounted.current) toast.error(`${name}：${error.message}`);
        },
    });
    const result = useQuery({
        queryKey: deviceKey(deviceId, 'locate-command', command?.id),
        queryFn: ({signal}) => getCommand(deviceId, command!.id, signal),
        enabled: Boolean(command),
        refetchInterval: (query) => query.state.data && finished(query.state.data.status) ? false : 1000,
    });
    const current = result.data || command;
    const waiting = mutation.isPending || Boolean(current && !finished(current.status));
    useEffect(() => {
        if (!current || !finished(current.status) || reported.current === current.id) return;
        reported.current = current.id;
        void queryClient.invalidateQueries({queryKey: ['devices']});
        void queryClient.invalidateQueries({queryKey: deviceKey(deviceId, 'status')});
        void queryClient.invalidateQueries({queryKey: deviceKey(deviceId, 'info')});
        void queryClient.invalidateQueries({queryKey: deviceKey(deviceId, 'commands')});
        if (current.status === 'succeeded') toast.success(`${name}：定位灯操作成功`);
        else if (current.status === 'failed') toast.error(`${name}：定位灯操作失败，请查看最近操作`);
        else toast.warning(`${name}：定位结果待核对，请检查设备状态`);
    }, [current, deviceId, name, queryClient]);

    return <Button
        type="button"
        variant="outline"
        size="sm"
        className={cn('shrink-0', locating && 'border-amber-300 bg-amber-50 text-amber-700 hover:bg-amber-100', className)}
        title={!supported ? '更新设备脚本后支持 GPIO21 定位灯' : !online ? '设备离线，恢复连接后可操作' : 'GPIO21 LED 每 500 毫秒切换亮灭'}
        aria-label={`${locating ? '停止定位' : '定位'} ${name}`}
        aria-pressed={Boolean(locating)}
        disabled={!online || !supported || waiting}
        onClick={() => mutation.mutate(!locating)}
    >
        {waiting ? <Loader2 className="size-3.5 animate-spin"/> : locating ? <Square className="size-3.5"/> : <MapPin className="size-3.5"/>}
        {waiting ? '等待设备' : locating ? '停止定位' : '定位'}
    </Button>;
}
