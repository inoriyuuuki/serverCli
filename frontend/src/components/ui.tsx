import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type SelectHTMLAttributes,
  type InputHTMLAttributes,
} from 'react';
import { cn, formatDateTime, utcString } from '../lib/format';

/* ---------------------------------- Card ---------------------------------- */

export function Card({
  title,
  actions,
  children,
  className,
  pad = true,
}: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  pad?: boolean;
}) {
  return (
    <section className={cn('card', className)}>
      {(title !== undefined || actions) && (
        <header className="card-head">
          <h2 className="card-title">{title}</h2>
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      <div className={pad ? 'card-body' : ''}>{children}</div>
    </section>
  );
}

/* ------------------------------ Page header ------------------------------- */

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1 className="page-title">{title}</h1>
        {subtitle && <div className="page-subtitle">{subtitle}</div>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </div>
  );
}

/* ------------------------------ State blocks ------------------------------ */

export function LoadingState({ label = '加载中…' }: { label?: string }) {
  return (
    <div className="state-block" role="status">
      <span className="spinner" aria-hidden />
      <p>{label}</p>
    </div>
  );
}

export function EmptyState({ title = '暂无数据', hint }: { title?: string; hint?: ReactNode }) {
  return (
    <div className="state-block state-empty">
      <div className="state-icon" aria-hidden>∅</div>
      <p>{title}</p>
      {hint && <p className="muted">{hint}</p>}
    </div>
  );
}

export function ErrorState({ title = '加载失败', message, onRetry }: { title?: string; message?: ReactNode; onRetry?: () => void }) {
  return (
    <div className="state-block state-error" role="alert">
      <div className="state-icon" aria-hidden>!</div>
      <p>{title}</p>
      {message && <p className="muted">{message}</p>}
      {onRetry && (
        <button className="btn btn-ghost" onClick={onRetry}>
          重试
        </button>
      )}
    </div>
  );
}

export function OfflineState({ message = '目标节点当前离线，部分功能不可用。' }: { message?: string }) {
  return (
    <div className="state-block state-offline" role="status">
      <div className="state-icon" aria-hidden>⛔</div>
      <p>节点离线</p>
      <p className="muted">{message}</p>
    </div>
  );
}

export function ForbiddenState({ message = '当前角色无权访问此内容。' }: { message?: string }) {
  return (
    <div className="state-block state-forbidden" role="alert">
      <div className="state-icon" aria-hidden>🔒</div>
      <p>权限不足</p>
      <p className="muted">{message}</p>
    </div>
  );
}

export function StaleBanner({ children }: { children: ReactNode }) {
  return <div className="banner banner-warn">{children}</div>;
}

/* -------------------------------- Badges ---------------------------------- */

export type Tone = 'gray' | 'blue' | 'green' | 'amber' | 'red' | 'indigo' | 'teal';

export function Badge({ tone = 'gray', children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <span className={cn('badge', `badge-${tone}`)} title={title}>
      {children}
    </span>
  );
}

export function StatusBadge({ status, title }: { status?: string | null; title?: string }) {
  const { label, tone } = statusMeta(status);
  return (
    <Badge tone={tone} title={title ?? status ?? undefined}>
      {label}
    </Badge>
  );
}

export function statusMeta(status?: string | null): { label: string; tone: Tone } {
  const s = (status ?? '').toLowerCase();
  const map: Record<string, { label: string; tone: Tone }> = {
    // nodes & enrollments
    pending: { label: '待审批', tone: 'amber' },
    approved: { label: '已批准', tone: 'green' },
    rejected: { label: '已拒绝', tone: 'red' },
    expired: { label: '已过期', tone: 'gray' },
    claimed: { label: '已领取', tone: 'indigo' },
    online: { label: '在线', tone: 'green' },
    degraded: { label: '降级', tone: 'amber' },
    offline: { label: '离线', tone: 'red' },
    disabled: { label: '已禁用', tone: 'gray' },
    // tasks
    queued: { label: '排队中', tone: 'blue' },
    dispatched: { label: '已下发', tone: 'indigo' },
    running: { label: '运行中', tone: 'green' },
    succeeded: { label: '成功', tone: 'green' },
    success: { label: '成功', tone: 'green' },
    failed: { label: '失败', tone: 'red' },
    failure: { label: '失败', tone: 'red' },
    timed_out: { label: '超时', tone: 'amber' },
    cancelled: { label: '已取消', tone: 'gray' },
    node_unreachable: { label: '节点失联', tone: 'red' },
    result_unknown: { label: '结果未知', tone: 'amber' },
    // leases
    active: { label: '生效中', tone: 'green' },
    disconnected: { label: '已断开', tone: 'gray' },
    revoked: { label: '已撤销', tone: 'red' },
    // audit
    denied: { label: '拒绝', tone: 'amber' },
    // misc
    enabled: { label: '启用', tone: 'green' },
    true: { label: '是', tone: 'green' },
    false: { label: '否', tone: 'gray' },
  };
  return map[s] ?? { label: status || '未知', tone: 'gray' };
}

export function RiskBadge({ risk }: { risk?: string | null }) {
  const r = (risk ?? '').toLowerCase();
  const tone: Tone = r.includes('high') || r === 'critical' ? 'red' : r.includes('medium') || r === 'medium' ? 'amber' : 'gray';
  const label = r === 'high' ? '高' : r === 'medium' ? '中' : r === 'low' ? '低' : r === 'critical' ? '严重' : risk || '—';
  return <Badge tone={tone}>{label}</Badge>;
}

export function ProfileBadge({ profile }: { profile?: string | null }) {
  const p = (profile ?? '').toLowerCase();
  const tone: Tone = p === 'admin' ? 'red' : p === 'operator' ? 'amber' : 'blue';
  const label =
    p === 'read-only' ? '只读' : p === 'operator' ? '运维' : p === 'admin' ? '管理' : profile || '—';
  return <Badge tone={tone}>{label}</Badge>;
}

/* -------------------------------- TimeCell -------------------------------- */

/** Local time display with UTC on hover (title). */
export function TimeCell({ value, fallback = '—' }: { value?: string | null; fallback?: string }) {
  if (!value) return <span className="muted">{fallback}</span>;
  const utc = utcString(value);
  return (
    <span className="time-cell" title={utc ? `UTC: ${utc}` : undefined}>
      {formatDateTime(value)}
    </span>
  );
}

/* --------------------------------- Modal ---------------------------------- */

export function Modal({
  open,
  title,
  onClose,
  children,
  width,
}: {
  open: boolean;
  title: ReactNode;
  onClose: () => void;
  children: ReactNode;
  width?: number;
}) {
  if (!open) return null;
  return (
    <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={typeof title === 'string' ? title : '对话框'} onClick={onClose}>
      <div
        className="modal"
        style={width ? { maxWidth: width } : undefined}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="modal-head">
          <h3>{title}</h3>
          <button className="btn-icon" onClick={onClose} aria-label="关闭">
            ✕
          </button>
        </header>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}

/* ------------------------------- Confirmation ----------------------------- */

export interface ConfirmOptions {
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  /** red styling + extra warning when writing to production */
  danger?: boolean;
  production?: boolean;
  requireReason?: boolean;
  reasonLabel?: string;
  reasonPlaceholder?: string;
}

interface ConfirmResult {
  ok: boolean;
  reason?: string;
}

const ConfirmContext = createContext<(opts: ConfirmOptions) => Promise<ConfirmResult>>(async () => ({ ok: false }));

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [opts, setOpts] = useState<ConfirmOptions | null>(null);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const resolverRef = useRef<((r: ConfirmResult) => void) | null>(null);

  const confirm = useCallback((options: ConfirmOptions) => {
    setReason('');
    setOpts(options);
    return new Promise<ConfirmResult>((resolve) => {
      resolverRef.current = resolve;
    });
  }, []);

  const close = useCallback((result: ConfirmResult) => {
    setOpts(null);
    setSubmitting(false);
    const r = resolverRef.current;
    resolverRef.current = null;
    if (r) r(result);
  }, []);

  const value = useMemo(() => confirm, [confirm]);

  const open = opts !== null;
  const needsReason = Boolean(opts?.requireReason);
  const canConfirm = !needsReason || reason.trim().length > 0;

  return (
    <ConfirmContext.Provider value={value}>
      {children}
      <Modal
        open={open}
        title={
          <span className={opts?.danger ? 'text-danger' : undefined}>
            {opts?.title ?? '确认'}
          </span>
        }
        onClose={() => close({ ok: false })}
        width={520}
      >
        {opts && (
          <div className="confirm-body">
            <div className="confirm-message">{opts.message}</div>
            {opts.production && (
              <div className="alert alert-danger" role="alert">
                ⚠️ <strong>正式环境</strong>：此操作将写入正式环境，请谨慎确认。
              </div>
            )}
            {needsReason && (
              <label className="field">
                <span className="field-label">
                  {opts.reasonLabel ?? '原因'} <em className="req">*</em>
                </span>
                <textarea
                  className="input"
                  rows={3}
                  value={reason}
                  placeholder={opts.reasonPlaceholder ?? '请填写操作原因（将写入审计日志）'}
                  onChange={(e) => setReason(e.target.value)}
                />
              </label>
            )}
            <div className="modal-actions">
              <button className="btn btn-ghost" onClick={() => close({ ok: false })} disabled={submitting}>
                {opts.cancelLabel ?? '取消'}
              </button>
              <button
                className={cn('btn', opts.danger ? 'btn-danger' : 'btn-primary')}
                disabled={!canConfirm || submitting}
                onClick={() => close({ ok: true, reason: reason.trim() || undefined })}
              >
                {opts.confirmLabel ?? '确认'}
              </button>
            </div>
          </div>
        )}
      </Modal>
    </ConfirmContext.Provider>
  );
}

export function useConfirm() {
  return useContext(ConfirmContext);
}

/* -------------------------------- Form bits ------------------------------- */

export function Field({
  label,
  required,
  hint,
  children,
}: {
  label: ReactNode;
  required?: boolean;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <label className="field">
      <span className="field-label">
        {label} {required && <em className="req">*</em>}
      </span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}

export function TextInput(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cn('input', props.className)} />;
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={cn('input', props.className)} />;
}

export function Checkbox({
  label,
  checked,
  onChange,
  disabled,
}: {
  label: ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <label className={cn('checkbox-row', disabled && 'is-disabled')}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>{label}</span>
    </label>
  );
}

/* --------------------------------- Tabs ----------------------------------- */

export function Tabs({
  tabs,
  active,
  onChange,
}: {
  tabs: Array<{ key: string; label: ReactNode; count?: number; disabled?: boolean }>;
  active: string;
  onChange: (key: string) => void;
}) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.key}
          role="tab"
          aria-selected={active === t.key}
          className={cn('tab', active === t.key && 'tab-active', t.disabled && 'is-disabled')}
          onClick={() => !t.disabled && onChange(t.key)}
          disabled={t.disabled}
        >
          {t.label}
          {typeof t.count === 'number' && <span className="tab-count">{t.count}</span>}
        </button>
      ))}
    </div>
  );
}

/* ------------------------------ Error alert ------------------------------- */

export function Alert({ tone = 'danger', children }: { tone?: 'danger' | 'warn' | 'info' | 'success'; children: ReactNode }) {
  return <div className={cn('alert', `alert-${tone}`)}>{children}</div>;
}
