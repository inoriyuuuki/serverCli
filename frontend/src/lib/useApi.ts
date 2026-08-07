import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError, type RequestOptions } from '../api/client';

export interface AsyncState<T> {
  data: T | null;
  loading: boolean;
  error: ApiError | null;
  /** true when data may be stale (e.g. node offline during refresh). */
  stale: boolean;
  reload: () => void;
  setData: (updater: T | ((prev: T | null) => T | null)) => void;
}

/**
 * Small fetch hook: distinguishes initial loading, empty data, and load
 * failures (never conflates "load failed" with "no data").
 */
export function useApi<T = unknown>(
  path: string | null,
  options?: { query?: RequestOptions['query']; deps?: unknown[]; enabled?: boolean },
): AsyncState<T> {
  const { query, deps = [], enabled = true } = options ?? {};
  const [data, setDataState] = useState<T | null>(null);
  const [loading, setLoading] = useState<boolean>(Boolean(path) && enabled);
  const [error, setError] = useState<ApiError | null>(null);
  const [stale, setStale] = useState(false);
  const [tick, setTick] = useState(0);
  const queryKey = JSON.stringify(query ?? {});
  const pathRef = useRef(path);
  const queryRef = useRef(query);
  const dataRef = useRef<T | null>(null);
  dataRef.current = data;

  pathRef.current = path;
  queryRef.current = query;

  const reload = useCallback(() => setTick((t) => t + 1), []);

  const setData = useCallback((updater: T | ((prev: T | null) => T | null)) => {
    setDataState((prev) => (typeof updater === 'function' ? (updater as (p: T | null) => T | null)(prev) : updater));
  }, []);

  useEffect(() => {
    if (!path || !enabled) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);
    if (dataRef.current !== null) setStale(true);
    api
      .get<T>(path, queryRef.current)
      .then((result) => {
        if (cancelled) return;
        setDataState(result);
        setStale(false);
      })
      .catch((err) => {
        if (cancelled) return;
        if (err instanceof ApiError) setError(err);
        else setError(new ApiError(0, 'UNKNOWN', '发生未知错误'));
        setStale(false);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, queryKey, tick, enabled, ...deps]);

  return { data, loading, error, stale, reload, setData };
}

export function errorMessage(err: ApiError | null, fallback = '请求失败'): string {
  if (!err) return fallback;
  if (err.code === 'NETWORK_ERROR') return '无法连接服务器：请检查网络或稍后重试（加载失败，并非没有数据）';
  return err.message || fallback;
}
