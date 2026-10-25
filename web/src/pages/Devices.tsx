import {useState} from 'react';
import {Link} from 'react-router-dom';
import {useMutation, useQuery, useQueryClient} from '@tanstack/react-query';
import {ArrowRight, Cable, KeyRound, Pencil, RadioTower, RefreshCw, Search, Smartphone} from 'lucide-react';
import {toast} from 'sonner';
import {getDevices, renameDevice, type DeviceRecord} from '@/api/devices';
import {Button} from '@/components/ui/button';
import {Input} from '@/components/ui/input';
import {Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogFooter} from '@/components/ui/dialog';
import {PageHeader} from '@/components/PageHeader';
import QueryError from '@/components/QueryError';
import SensitiveValue from '@/components/SensitiveValue';
import LocateButton from '@/components/LocateButton';
import {cn} from '@/lib/utils';

export default function Devices() {
    const queryClient = useQueryClient();
    const query = useQuery({queryKey: ['devices'], queryFn: ({signal}) => getDevices(signal), refetchInterval: 10000});
    const [search, setSearch] = useState('');
    const [mode, setMode] = useState('all');
    const [editing, setEditing] = useState<DeviceRecord | null>(null);
    const [name, setName] = useState('');
    const rename = useMutation({
        mutationFn: ({id, name}: {id: string; name: string}) => renameDevice(id, name),
        onSuccess: (_, {id}) => {
            void queryClient.invalidateQueries({queryKey: ['devices']});
            void queryClient.invalidateQueries({queryKey: ['device', id, 'info']});
            setEditing(null);
            toast.success('设备名称已更新');
        },
        onError: (error) => toast.error(error.message),
    });
    const devices = query.data || [];
    const filtered = devices.filter((d) => (mode === 'all' || d.accessMode === mode) &&
        [d.id, d.name, d.features?.imei, d.status?.mobile?.number].some((v) => String(v || '').toLowerCase().includes(search.toLowerCase())));

    return <div>
        <PageHeader title="设备管理" description="查看已认证的设备，选择一台设备进入独立的短信与控制台。" action={<div className="flex gap-2">
            <Button variant="outline" onClick={() => void query.refetch()} disabled={query.isFetching}><RefreshCw className={cn('size-4', query.isFetching && 'animate-spin')}/>刷新</Button>
            <Button asChild><Link to="/devices/enroll"><KeyRound className="size-4"/>接入设备</Link></Button>
        </div>}/>
        <div className="mt-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
            {[['全部设备', devices.length], ['在线设备', devices.filter((d) => d.online).length], ['USB 串口', devices.filter((d) => d.accessMode === 'serial').length], ['蜂窝 MQTT', devices.filter((d) => d.accessMode === 'mqtt').length]].map(([label, value]) =>
                <div key={label} className="rounded-2xl border border-slate-200 bg-white p-5"><p className="text-xs font-medium text-slate-500">{label}</p><p className="mt-2 text-3xl font-bold tracking-tight text-[#0b2a55]">{query.isPending || query.isError ? '—' : value}</p></div>)}
        </div>
        <div className="mt-5 flex flex-wrap items-center gap-3">
            <div className="relative min-w-0 flex-1"><Search className="absolute left-3 top-3 size-4 text-slate-400"/><Input aria-label="搜索设备" placeholder="搜索名称、设备 ID 或手机号" value={search} onChange={(e) => setSearch(e.target.value)} className="pl-9"/></div>
            <select aria-label="接入方式" value={mode} onChange={(e) => setMode(e.target.value)} className="h-10 rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700"><option value="all">全部接入方式</option><option value="serial">USB 串口</option><option value="mqtt">蜂窝 MQTT</option></select>
        </div>
        {query.isError && <QueryError error={query.error} retry={() => void query.refetch()}/>}
        {query.isPending ? <p className="flex items-center gap-2 py-16 text-sm text-slate-500"><RefreshCw className="size-4 animate-spin"/>正在获取设备列表</p> : filtered.length === 0 ?
            <div className="mt-5 rounded-2xl border border-dashed border-slate-300 bg-white px-6 py-16 text-center"><Smartphone className="mx-auto size-10 text-blue-200"/><h2 className="mt-4 text-base font-semibold text-slate-800">{devices.length ? '没有匹配的设备' : query.isError ? '设备列表暂不可用' : '暂无已认证设备'}</h2><p className="mx-auto mt-2 max-w-lg text-sm leading-6 text-slate-500">{devices.length ? '尝试其他名称或接入方式。' : 'USB 设备会在后端识别后出现。MQTT 设备完成注册认证后，会自动加入列表。'}</p></div> :
            <div className="mt-5 grid gap-4 md:grid-cols-2 xl:grid-cols-3">{filtered.map((d) => {
                const mobile = d.status?.mobile || d.features?.mobile;
                const displayName = d.name && d.name !== d.id && d.name !== d.features?.imei ? d.name : '未命名设备';
                const Icon = d.accessMode === 'mqtt' ? RadioTower : Cable;
                return <article key={d.id} className="overflow-hidden rounded-2xl border border-slate-200 bg-white">
                    <div className="flex items-start gap-3 p-5"><span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-blue-50 text-blue-700"><Icon className="size-5"/></span>
                        <div className="min-w-0 flex-1"><Link to={`/devices/${encodeURIComponent(d.id)}`} className="block truncate font-semibold text-slate-900 hover:text-blue-700">{displayName}</Link><div className="mt-1 text-xs text-slate-400"><SensitiveValue value={d.features?.imei || d.id} label={`${displayName} IMEI`}/></div></div>
                        <button type="button" aria-label={`重命名 ${displayName}`} onClick={() => {setEditing(d); setName(d.name || d.id);}} className="rounded-lg p-2 text-slate-400 hover:bg-slate-50 hover:text-blue-700"><Pencil className="size-4"/></button>
                    </div>
                    <dl className="space-y-3 px-5 pb-5 text-xs">
                        {[['接入方式', d.accessMode === 'mqtt' ? '蜂窝 MQTT' : 'USB 串口'], ['手机号', mobile?.number || '未读取到'], ['模组', d.features?.model || '—'], ['最后通信', d.lastSeen ? new Date(d.lastSeen).toLocaleString('zh-CN') : '—']].map(([label, value]) => <div key={label} className="flex min-w-0 justify-between gap-3"><dt className="shrink-0 text-slate-400">{label}</dt><dd className="truncate text-right font-medium text-slate-700" title={label === '手机号' ? undefined : value}>{label === '手机号' ? <SensitiveValue value={mobile?.number} label={`${displayName} 手机号`}/> : value}</dd></div>)}
                    </dl>
                    <div className="flex items-center justify-between border-t border-slate-100 bg-slate-50/60 px-5 py-3"><span className={cn('flex items-center gap-1.5 text-xs font-semibold', query.isError ? 'text-slate-400' : d.online ? 'text-emerald-600' : 'text-slate-500')}><span className={cn('size-1.5 rounded-full', !query.isError && d.online ? 'bg-emerald-500' : 'bg-slate-300')}/>{query.isError ? '状态未更新' : d.online ? '在线' : '离线'}</span><div className="flex items-center gap-3"><LocateButton deviceId={d.id} name={displayName} online={d.online && !query.isError} supported={d.features?.locate_led === true} locating={d.status?.locating}/><Link to={`/devices/${encodeURIComponent(d.id)}`} className="flex items-center gap-1 text-xs font-semibold text-blue-700">{d.online ? '进入设备' : '查看设备'}<ArrowRight className="size-3.5"/></Link></div></div>
                </article>;
            })}</div>}
        <Dialog open={Boolean(editing)} onOpenChange={(open) => {if (!open && !rename.isPending) setEditing(null);}}><DialogContent><DialogHeader><DialogTitle>修改设备名称</DialogTitle><DialogDescription>只修改显示名称，设备 ID 保持不变。</DialogDescription></DialogHeader><form onSubmit={(e) => {e.preventDefault(); if (editing && name.trim()) rename.mutate({id: editing.id, name: name.trim()});}}><Input aria-label="设备名称" value={name} onChange={(e) => setName(e.target.value)} maxLength={128}/><DialogFooter className="mt-5"><Button type="submit" disabled={rename.isPending || !name.trim()}>{rename.isPending ? '保存中…' : '保存名称'}</Button></DialogFooter></form></DialogContent></Dialog>
    </div>;
}
