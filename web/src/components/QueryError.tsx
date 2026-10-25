import {Button} from './ui/button';

export default function QueryError({error, retry}: {error: unknown; retry?: () => void}) {
    return <div role="alert" className="my-4 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-rose-200 bg-rose-50 p-4 text-sm text-rose-800">
        <span>{error instanceof Error ? error.message : '数据加载失败，请重试'}</span>
        {retry && <Button variant="outline" size="sm" onClick={retry}>重试</Button>}
    </div>;
}
