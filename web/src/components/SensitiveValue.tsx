import {useState} from 'react';
import {Eye, EyeOff} from 'lucide-react';
import {cn} from '@/lib/utils';

interface Props {
    value?: string | null;
    label: string;
    fallback?: string;
    className?: string;
}

function Value({value, label, fallback = '未读取到', className}: Props) {
    const [visible, setVisible] = useState(false);
    if (!value) return <span className={className}>{fallback}</span>;
    const action = visible ? '隐藏' : '显示';
    return <button
        type="button"
        aria-label={`${action} ${label}`}
        aria-pressed={visible}
        title={`${action}${label}`}
        onClick={() => setVisible((current) => !current)}
        className={cn('inline-flex max-w-full items-center gap-1.5 rounded px-0.5 py-0.5 font-mono hover:bg-blue-50 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-blue-500', className)}
    >
        <span className="truncate">{visible ? value : '•••••••••••'}</span>
        {visible ? <EyeOff aria-hidden="true" className="size-3.5 shrink-0 text-slate-400"/> : <Eye aria-hidden="true" className="size-3.5 shrink-0 text-slate-400"/>}
    </button>;
}

// Changing identity or the underlying value remounts the local visibility state.
export default function SensitiveValue(props: Props) {
    return <Value key={`${props.label}:${props.value || ''}`} {...props}/>;
}
