import {useEffect, useMemo, useRef} from 'react';
import {Link, useParams} from 'react-router-dom';
import {useQuery, useQueryClient} from '@tanstack/react-query';
import {LoaderCircle} from 'lucide-react';
import {DeviceContext} from '@/context/DeviceContext';
import {deviceKey, getDevice} from '@/api/devices';
import Layout from './Layout';
import QueryError from './QueryError';

function Scope({deviceId}: {deviceId: string}) {
    const active = useRef(true);
    const queryClient = useQueryClient();
    const query = useQuery({queryKey: deviceKey(deviceId, 'info'), queryFn: ({signal}) => getDevice(deviceId, signal), refetchInterval: 10000, retry: false});
    useEffect(() => {
        active.current = true;
        return () => {
            active.current = false;
            // StrictMode remounts effects; only clear on an actual route departure.
            queueMicrotask(() => {
                if (!active.current) {
                    void queryClient.cancelQueries({queryKey: deviceKey(deviceId)});
                    queryClient.removeQueries({queryKey: deviceKey(deviceId)});
                }
            });
        };
    }, [deviceId, queryClient]);
    const value = useMemo(() => query.data ? {deviceId, device: query.data, isActive: () => active.current} : null, [deviceId, query.data]);
    if (!query.data) return <main className="grid min-h-screen place-items-center bg-[#f3f6f9] p-6">
        <div className="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6">
            <Link className="text-sm font-semibold text-blue-700" to="/devices">返回设备列表</Link>
            {query.isError ? <QueryError error={query.error} retry={() => void query.refetch()}/> : <p className="mt-5 flex items-center gap-2 text-sm text-slate-500"><LoaderCircle className="size-4 animate-spin"/>正在加载设备</p>}
        </div>
    </main>;
    return <DeviceContext.Provider value={value}><Layout/></DeviceContext.Provider>;
}

export default function DeviceScope() {
    const {deviceId = ''} = useParams();
    return <Scope key={deviceId} deviceId={deviceId}/>;
}
