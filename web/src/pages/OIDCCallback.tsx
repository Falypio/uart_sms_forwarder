import {useEffect, useRef} from 'react';
import {useNavigate, useSearchParams} from 'react-router-dom';
import {useQueryClient} from '@tanstack/react-query';
import {oidcLogin, type LoginResponse} from '@/api/auth';
import {toast} from 'sonner';

export default function OIDCCallback() {
    const navigate = useNavigate();
    const [params] = useSearchParams();
    const client = useQueryClient();
    const request = useRef<{key: string; promise: Promise<LoginResponse>} | null>(null);
    const code = params.get('code');
    const state = params.get('state');
    const error = params.get('error');
    const description = params.get('error_description');
    useEffect(() => {
        if (error || !code || !state) {
            toast.error(description || error || '缺少认证参数');
            navigate('/login', {replace: true});
            return;
        }
        let cancelled = false;
        const key = `${code}:${state}`;
        // Reuse the exchange in StrictMode rather than consuming an OIDC code twice.
        if (request.current?.key !== key) request.current = {key, promise: oidcLogin(code, state)};
        void request.current.promise.then((result) => {
            if (cancelled) return;
            client.clear();
            localStorage.setItem('token', result.token);
            localStorage.setItem('username', result.username);
            toast.success('登录成功');
            navigate('/devices', {replace: true});
        }).catch((reason: unknown) => {
            if (cancelled) return;
            toast.error(reason instanceof Error ? reason.message : 'OIDC 认证失败');
            navigate('/login', {replace: true});
        });
        return () => {cancelled = true;};
    }, [code, state, error, description, navigate, client]);
    return <main className="grid min-h-screen place-items-center bg-[#f3f6f9]"><p className="text-sm text-slate-500">正在验证登录身份…</p></main>;
}