import {useQuery} from '@tanstack/react-query';
import {deviceKey, getCommands} from '@/api/devices';
import {useDevice} from '@/context/DeviceContext';
import QueryError from './QueryError';
import {cn} from '@/lib/utils';

const actions: Record<string, string> = {set_locate: '切换定位灯', send_sms: '发送短信', get_status: '查询状态', set_flymode: '切换飞行模式', reset_stack: '重置协议栈', reboot_mcu: '重启模组'};
const states = {pending: '等待设备', accepted: '已接收', queued: '排队中', succeeded: '执行成功', failed: '执行失败', unknown: '结果待核对'};

export default function CommandHistory() {
    const {deviceId} = useDevice();
    const query = useQuery({queryKey: deviceKey(deviceId, 'commands'), queryFn: ({signal}) => getCommands(deviceId, signal), refetchInterval: 3000});
    return <section className="mt-5 rounded-2xl border border-slate-200 bg-white p-5">
        <h2 className="text-sm font-semibold text-slate-900">最近操作</h2>
        <p className="mt-1 text-xs leading-5 text-slate-500">“已接收”表示设备已接收请求；最终结果以设备回报为准。结果不确定时，请核对后再操作。</p>
        {query.isError && <QueryError error={query.error} retry={() => void query.refetch()}/>}
        {!query.data?.length ? <p className="py-6 text-xs text-slate-400">{query.isPending ? '读取中…' : '暂无操作记录'}</p> : <div className="mt-4 overflow-x-auto"><table className="w-full min-w-[480px] text-left text-xs"><thead className="border-b border-slate-100 text-slate-400"><tr><th className="pb-3 font-medium">操作</th><th className="pb-3 font-medium">请求编号</th><th className="pb-3 font-medium">时间</th><th className="pb-3 font-medium">结果</th></tr></thead><tbody>{query.data.slice(0, 10).map((cmd) => <tr key={cmd.id} className="border-b border-slate-50 last:border-0"><td className="py-3 font-medium text-slate-700">{actions[cmd.action] || cmd.action}</td><td className="py-3 font-mono text-slate-400" title={cmd.id}>{cmd.id.slice(-12)}</td><td className="py-3 text-slate-500">{new Date(cmd.createdAt).toLocaleString('zh-CN')}</td><td className={cn('py-3 font-semibold', cmd.status === 'succeeded' ? 'text-emerald-600' : cmd.status === 'failed' ? 'text-rose-600' : cmd.status === 'unknown' ? 'text-amber-600' : 'text-blue-600')}>{states[cmd.status] || cmd.status}</td></tr>)}</tbody></table></div>}
    </section>;
}
