import {useEffect, useRef, useState} from 'react';
import {useQuery, useQueryClient} from '@tanstack/react-query';
import {Copy, Download, Eye, EyeOff, KeyRound, LoaderCircle, ShieldCheck, Unplug} from 'lucide-react';
import {toast} from 'sonner';
import {downloadText, enrollDevice, getDevices, getPublicKey, revokeDevice, type Enrollment} from '@/api/devices';
import {PageHeader} from '@/components/PageHeader';
import QueryError from '@/components/QueryError';
import {Button} from '@/components/ui/button';
import {Input} from '@/components/ui/input';
import {Textarea} from '@/components/ui/textarea';
import {Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogFooter} from '@/components/ui/dialog';

export default function DeviceEnrollment() {
    const client = useQueryClient();
    const alive = useRef(true);
    const [deviceId, setDeviceId] = useState('');
    const [tokenVisible, setTokenVisible] = useState(false);
    const tokenInput = useRef<HTMLInputElement>(null);
    const [replace, setReplace] = useState(false);
    const [busy, setBusy] = useState(false);
    // Registration secrets stay in this mounted page only; never in query cache/localStorage.
    const [issued, setIssued] = useState<Enrollment | null>(null);
    const [revokeId, setRevokeId] = useState('');
    const [confirmRevoke, setConfirmRevoke] = useState(false);
    const key = useQuery({queryKey: ['server-public-key'], queryFn: ({signal}) => getPublicKey(signal)});
    const devices = useQuery({queryKey: ['devices'], queryFn: ({signal}) => getDevices(signal), refetchInterval: 10000});
    useEffect(() => {alive.current = true; return () => {alive.current = false;};}, []);
    const existing = devices.data?.some((d) => d.id === deviceId.trim());
    const valid = /^[A-Za-z0-9_-]{1,64}$/.test(deviceId.trim());
    const issue = async () => {
        if (!valid || busy || !replace) return;
        setBusy(true); setIssued(null); setTokenVisible(false);
        try {
            const result = await enrollDevice(deviceId.trim());
            void client.invalidateQueries({queryKey: ['devices']});
            if (alive.current) {setIssued(result); toast.success('设备注册配置已生成');}
        } catch (error) {if (alive.current) toast.error(error instanceof Error ? error.message : '生成失败');}
        finally {if (alive.current) setBusy(false);}
    };
    const copyToken = async () => {
        if (!issued) return;
        try {
            if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
            await navigator.clipboard.writeText(issued.registrationToken);
            if (alive.current) toast.success('注册凭据已复制');
        } catch {
            if (!alive.current) return;
            // Plain HTTP LAN deployments may not expose the Clipboard API.
            setTokenVisible(true);
            requestAnimationFrame(() => {
                if (!alive.current) return;
                tokenInput.current?.focus();
                tokenInput.current?.select();
                toast.info('已选中注册凭据，请按 Ctrl+C 或长按复制');
            });
        }
    };
    const revoke = async () => {
        if (busy || !/^[A-Za-z0-9_-]{1,64}$/.test(revokeId.trim())) return;
        setBusy(true);
        try {
            const id = revokeId.trim();
            await revokeDevice(id);
            void client.invalidateQueries({queryKey: ['devices']});
            void client.invalidateQueries({queryKey: ['device', id]});
            if (alive.current) {setConfirmRevoke(false); if (issued?.deviceId === id) setIssued(null); toast.success('设备 MQTT 业务凭据已撤销');}
        } catch (error) {if (alive.current) toast.error(error instanceof Error ? error.message : '撤销失败');}
        finally {if (alive.current) setBusy(false);}
    };
    return <div>
        <PageHeader title="设备接入" description="USB 设备由后端自动发现；MQTT 设备使用独立凭据完成注册。"/>
        <div className="mt-6 grid items-start gap-5 xl:grid-cols-[1.2fr_0.8fr]">
            <section className="rounded-2xl border border-slate-200 bg-white p-5 sm:p-6">
                <h2 className="flex items-center gap-2 text-base font-semibold text-slate-900"><KeyRound className="size-5 text-blue-700"/>MQTT 注册配置</h2>
                <p className="mt-2 text-sm leading-6 text-slate-500">设备 ID 与脚本保持一致；脚本未指定 ID 时使用模组 IMEI。账号密码需另外在 EMQX 中配置。</p>
                <form className="mt-5 space-y-4" onSubmit={(e) => {e.preventDefault(); void issue();}}>
                    <div><label htmlFor="enroll-device-id" className="mb-2 block text-sm font-medium text-slate-700">设备 ID</label><Input id="enroll-device-id" value={deviceId} onChange={(e) => {setDeviceId(e.target.value); setReplace(false);}} placeholder="例如模组 IMEI" maxLength={64} disabled={busy}/><p className="mt-2 text-xs text-slate-400">支持 1–64 个字母、数字、下划线或横线。</p></div>
                    {existing && <p className="rounded-xl border border-amber-200 bg-amber-50 p-3 text-xs leading-5 text-amber-800">这台设备已登记。重新生成会轮换注册凭据，并使其现有 MQTT 会话失效。</p>}
                    <label className="flex items-start gap-2 text-xs leading-5 text-slate-600"><input type="checkbox" checked={replace} disabled={busy} onChange={(e) => setReplace(e.target.checked)} className="mt-1 accent-blue-700"/>我确认设备 ID 正确；若此前已生成配置，将使用新凭据更新设备脚本。</label>
                    <Button type="submit" disabled={busy || !valid || !replace}>{busy ? <LoaderCircle className="size-4 animate-spin"/> : <ShieldCheck className="size-4"/>}生成注册配置</Button>
                </form>
                {issued && <div className="mt-6 space-y-4 rounded-xl border border-blue-100 bg-blue-50/50 p-4">
                    <div><h3 className="text-sm font-semibold text-blue-950">配置已生成：{issued.deviceId}</h3><p className="mt-1 text-xs leading-5 text-blue-700">注册凭据仅本次生成后可查看，默认隐藏。离开页面后不再保留；已保存的注册配置文件包含完整凭据。</p></div>
                    <div>
                        <label htmlFor="registration-token" className="mb-1.5 block text-xs font-medium text-slate-600">注册凭据</label>
                        <div className="relative">
                            <Input ref={tokenInput} id="registration-token" type={tokenVisible ? 'text' : 'password'} readOnly value={issued.registrationToken} autoComplete="off" spellCheck={false} className="pr-12 font-mono"/>
                            <button type="button" aria-label={tokenVisible ? '隐藏注册凭据' : '显示注册凭据'} aria-pressed={tokenVisible} aria-controls="registration-token" onClick={() => setTokenVisible((value) => !value)} className="absolute inset-y-0 right-1 flex w-9 items-center justify-center rounded text-slate-500 hover:text-blue-700 focus-visible:outline-2 focus-visible:outline-blue-500">
                                {tokenVisible ? <EyeOff aria-hidden="true" className="size-4"/> : <Eye aria-hidden="true" className="size-4"/>}
                            </button>
                        </div>
                        <div className="mt-2 flex flex-wrap items-center gap-2"><Button type="button" variant="outline" size="sm" onClick={() => void copyToken()}><Copy className="size-3.5"/>复制注册凭据</Button><span className="text-xs text-slate-500">点击眼睛图标查看完整凭据。</span></div>
                    </div>
                    <dl className="space-y-2 text-xs text-slate-600"><div>公钥版本：<span className="font-mono">{issued.keyId}</span></div><div className="break-all">主题前缀：<span className="font-mono">{issued.topicPrefix}</span></div></dl>
                    <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={() => downloadText('server_public.pem', issued.publicKey)}><Download className="size-4"/>下载公钥</Button><Button onClick={() => downloadText(`device-${issued.deviceId}-enrollment.json`, JSON.stringify(issued, null, 2))}><Download className="size-4"/>保存注册配置</Button></div>
                    <p className="text-xs leading-5 text-slate-500">配置文件包含设备注册凭据，请妥善保管。将相应字段填入 device_config.lua，再将公钥与全部 Lua 文件一起烧录。</p>
                </div>}
            </section>
            <div className="space-y-5">
                <section className="rounded-2xl border border-slate-200 bg-white p-5"><h2 className="text-sm font-semibold text-slate-900">服务器公钥</h2><p className="mt-2 text-xs leading-5 text-slate-500">用于消息内容加密，区别于 MQTT TLS 的 CA 证书。</p>{key.isError ? <QueryError error={key.error} retry={() => void key.refetch()}/> : <><p className="mt-3 text-xs text-slate-400">版本：{key.data?.keyId || '读取中…'}</p><Textarea aria-label="服务器公钥" readOnly value={key.data?.publicKey || ''} className="mt-3 h-32 font-mono text-[10px]"/><Button variant="outline" className="mt-3" disabled={!key.data} onClick={() => key.data && downloadText('server_public.pem', key.data.publicKey)}><Download className="size-4"/>下载服务器公钥</Button></>}</section>
                <section className="rounded-2xl border border-slate-200 bg-white p-5"><h2 className="text-sm font-semibold text-slate-900">接入步骤</h2><ol className="mt-3 list-decimal space-y-2 pl-4 text-xs leading-6 text-slate-500"><li>生成并保存设备专属注册配置。</li><li>在 EMQX 配置同名设备账号和独立密码。</li><li>填写 MQTT 地址、用户名密码及公钥；TLS 按需开启。</li><li>烧录并启动，认证成功后设备自动出现在列表。</li></ol></section>
                <section className="rounded-2xl border border-rose-100 bg-white p-5"><h2 className="flex items-center gap-2 text-sm font-semibold text-slate-900"><Unplug className="size-4 text-rose-500"/>撤销 MQTT 业务认证</h2><p className="mt-2 text-xs leading-5 text-slate-500">使该设备的注册凭据和现有会话失效，历史记录保留。EMQX 账号需另外停用。</p><Input aria-label="待撤销设备 ID" value={revokeId} onChange={(e) => setRevokeId(e.target.value)} placeholder="输入设备 ID" list="mqtt-device-ids" className="mt-3" disabled={busy}/><datalist id="mqtt-device-ids">{devices.data?.filter((d) => d.accessMode === 'mqtt').map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}</datalist><Button variant="outline" className="mt-3 border-rose-200 text-rose-600" disabled={busy || !/^[A-Za-z0-9_-]{1,64}$/.test(revokeId.trim())} onClick={() => setConfirmRevoke(true)}>撤销认证</Button></section>
            </div>
        </div>
        <Dialog open={confirmRevoke} onOpenChange={(open) => {if (!busy) setConfirmRevoke(open);}}><DialogContent><DialogHeader><DialogTitle>撤销设备认证？</DialogTitle><DialogDescription>设备 {revokeId.trim()} 将无法继续通过现有凭据和会话通信。历史短信和配置保留。</DialogDescription></DialogHeader><DialogFooter><Button variant="outline" disabled={busy} onClick={() => setConfirmRevoke(false)}>取消</Button><Button variant="destructive" disabled={busy} onClick={() => void revoke()}>{busy ? '处理中…' : '确认撤销'}</Button></DialogFooter></DialogContent></Dialog>
    </div>;
}
