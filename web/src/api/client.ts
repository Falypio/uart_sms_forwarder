const BASE_URL = '/api';

interface RequestOptions extends RequestInit {
    params?: Record<string, unknown>;
}

export class ApiError extends Error {
    readonly status: number;
    constructor(message: string, status: number) {
        super(message);
        this.name = 'ApiError';
        this.status = status;
    }
}

class ApiClient {
    constructor(private readonly baseURL: string) {}

    private buildURL(path: string, params?: Record<string, unknown>): string {
        const url = new URL(this.baseURL + path, window.location.origin);
        for (const [key, value] of Object.entries(params || {})) {
            if (value !== undefined && value !== null && value !== '') url.searchParams.append(key, String(value));
        }
        return url.toString();
    }

    private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
        const {params, ...fetchOptions} = options;
        const token = localStorage.getItem('token');
        const headers = new Headers(fetchOptions.headers);
        headers.set('Content-Type', 'application/json');
        if (token) headers.set('Authorization', `Bearer ${token}`);
        const response = await fetch(this.buildURL(path, params), {...fetchOptions, headers});

        if (response.status === 401) {
            // An old request must not log out a different, newly authenticated session.
            if (localStorage.getItem('token') === token) {
                localStorage.removeItem('token');
                localStorage.removeItem('username');
                window.location.assign('/login');
            }
            throw new ApiError('未授权，请重新登录', 401);
        }
        if (!response.ok) {
            const text = await response.text();
            let message = text || `HTTP ${response.status}: ${response.statusText}`;
            try {
                const body = JSON.parse(text) as {error?: string; message?: string};
                message = body.error || body.message || message;
            } catch { /* Preserve plain text errors. */ }
            throw new ApiError(message, response.status);
        }
        if (response.status === 204) return undefined as T;
        const text = await response.text();
        return text ? JSON.parse(text) as T : undefined as T;
    }

    get<T>(path: string, options?: RequestOptions): Promise<T> {
        return this.request<T>(path, {...options, method: 'GET'});
    }
    post<T>(path: string, data?: unknown, options?: RequestOptions): Promise<T> {
        return this.request<T>(path, {...options, method: 'POST', body: JSON.stringify(data)});
    }
    put<T>(path: string, data?: unknown, options?: RequestOptions): Promise<T> {
        return this.request<T>(path, {...options, method: 'PUT', body: JSON.stringify(data)});
    }
    delete<T>(path: string, options?: RequestOptions): Promise<T> {
        return this.request<T>(path, {...options, method: 'DELETE'});
    }
}

export default new ApiClient(BASE_URL);