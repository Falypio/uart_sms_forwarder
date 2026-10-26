import type {MobileInfo} from '@/api/types';

// Chinese MCC/MNC assignments listed in LuatOS's official APN configuration.
// One NZ: ITU Operational Bulletin 1280 (530 01 and 530 04).
// IMSI describes the SIM's home network, not necessarily the serving network.
const simOperators: Record<string, string> = {
    '46000': '中国移动', '46002': '中国移动', '46004': '中国移动',
    '46007': '中国移动', '46008': '中国移动', '46013': '中国移动',
    '46024': '中国移动', '46026': '中国移动',
    '46001': '中国联通', '46006': '中国联通', '46009': '中国联通',
    '46003': '中国电信', '46005': '中国电信', '46011': '中国电信',
    '46015': '中国广电',
    '53001': 'One NZ(新西兰)', '53004': 'One NZ(新西兰)',
};

export function describeOperator(mobile?: Partial<MobileInfo>): string {
    if (!mobile) return '等待运营商信息';
    if (mobile.sim_ready === false) return 'SIM 未就绪';
    const reported = typeof mobile.operator === 'string' ? mobile.operator.trim() : '';
    if (reported && !['unknown', 'null', 'undefined', '-', '—'].includes(reported.toLowerCase())) return reported;
    const imsi = typeof mobile.imsi === 'string' ? mobile.imsi.trim() : '';
    if (!/^\d{5,15}$/.test(imsi)) return '等待运营商信息';
    const inferred = simOperators[imsi.slice(0, 5)];
    return inferred || '未识别运营商';
}
