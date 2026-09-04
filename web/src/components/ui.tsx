import {
  AlertCircle,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Inbox,
  LoaderCircle,
  X,
} from "lucide-react";
import * as PopoverPrimitive from "@radix-ui/react-popover";
import * as SelectPrimitive from "@radix-ui/react-select";
import {
  createContext,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";

export function cx(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(" ");
}

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: "sm" | "md";
  loading?: boolean;
}

export function Button({ className, variant = "secondary", size = "md", loading, children, disabled, ...props }: ButtonProps) {
  return (
    <button
      type="button"
      className={cx("button", `button-${variant}`, `button-${size}`, className)}
      disabled={disabled || loading}
      {...props}
    >
      {loading ? <LoaderCircle className="spin" size={15} aria-hidden="true" /> : null}
      {children}
    </button>
  );
}

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string;
  danger?: boolean;
}

export function IconButton({ label, danger, className, children, ...props }: IconButtonProps) {
  return (
    <button
      type="button"
      className={cx("icon-button", danger && "icon-button-danger", className)}
      aria-label={label}
      title={label}
      {...props}
    >
      {children}
    </button>
  );
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cx("input", className)} {...props} />;
}

export interface SelectOption {
  value: string;
  label: string;
  description?: string;
  disabled?: boolean;
}

interface SelectMenuProps {
  value: string;
  onChange: (value: string) => void;
  options: SelectOption[];
  label: string;
  placeholder?: string;
  icon?: ReactNode;
  className?: string;
  disabled?: boolean;
}

const emptySelectValue = "__s2am_empty_value__";

export function SelectMenu({ value, onChange, options, label, placeholder = "请选择", icon, className, disabled }: SelectMenuProps) {
  const selected = options.find((option) => option.value === value);
  return (
    <div className={cx("select-control", className)}>
      {icon ? <span className="select-leading" aria-hidden="true">{icon}</span> : null}
      <SelectPrimitive.Root
        value={value === "" ? emptySelectValue : value}
        onValueChange={(next) => onChange(next === emptySelectValue ? "" : next)}
        disabled={disabled}
      >
        <SelectPrimitive.Trigger className="select-trigger" aria-label={label} title={selected?.label ?? placeholder}>
          <span className={cx("select-value", !selected && "select-placeholder")}>{selected?.label ?? placeholder}</span>
          <SelectPrimitive.Icon className="select-chevron"><ChevronDown size={14} /></SelectPrimitive.Icon>
        </SelectPrimitive.Trigger>
        <SelectPrimitive.Portal>
          <SelectPrimitive.Content className="select-content" position="popper" sideOffset={5} collisionPadding={12}>
            <SelectPrimitive.ScrollUpButton className="select-scroll-button"><ChevronUp size={14} /></SelectPrimitive.ScrollUpButton>
            <SelectPrimitive.Viewport className="select-viewport">
              {options.map((option) => {
                const optionValue = option.value === "" ? emptySelectValue : option.value;
                return (
                  <SelectPrimitive.Item className="select-item" value={optionValue} disabled={option.disabled} key={optionValue}>
                    <SelectPrimitive.ItemIndicator className="select-item-indicator"><Check size={14} /></SelectPrimitive.ItemIndicator>
                    <span className="select-item-copy">
                      <SelectPrimitive.ItemText>{option.label}</SelectPrimitive.ItemText>
                      {option.description ? <small>{option.description}</small> : null}
                    </span>
                  </SelectPrimitive.Item>
                );
              })}
            </SelectPrimitive.Viewport>
            <SelectPrimitive.ScrollDownButton className="select-scroll-button"><ChevronDown size={14} /></SelectPrimitive.ScrollDownButton>
          </SelectPrimitive.Content>
        </SelectPrimitive.Portal>
      </SelectPrimitive.Root>
    </div>
  );
}

interface ComboboxProps {
  value: string;
  onChange: (value: string) => void;
  options: SelectOption[];
  label: string;
  placeholder?: string;
  maxLength?: number;
  disabled?: boolean;
}

export function Combobox({ value, onChange, options, label, placeholder, maxLength, disabled }: ComboboxProps) {
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const listID = useId();
  const normalized = value.trim().toLowerCase();
  const visible = useMemo(() => options.filter((option) => {
    if (!normalized) return true;
    return `${option.label} ${option.value} ${option.description ?? ""}`.toLowerCase().includes(normalized);
  }).slice(0, 100), [normalized, options]);

  useEffect(() => setActiveIndex(0), [normalized]);

  function choose(option: SelectOption) {
    onChange(option.value);
    setOpen(false);
  }

  return (
    <PopoverPrimitive.Root open={open} onOpenChange={setOpen}>
      <PopoverPrimitive.Anchor asChild>
        <div className={cx("combobox", open && "combobox-open")}>
          <input
            className="combobox-input"
            value={value}
            onChange={(event) => { onChange(event.target.value); setOpen(true); }}
            onFocus={() => setOpen(true)}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown") {
                event.preventDefault();
                setOpen(true);
                setActiveIndex((current) => Math.min(current + 1, Math.max(visible.length - 1, 0)));
              } else if (event.key === "ArrowUp") {
                event.preventDefault();
                setActiveIndex((current) => Math.max(current - 1, 0));
              } else if (event.key === "Enter" && open && visible[activeIndex]) {
                event.preventDefault();
                choose(visible[activeIndex]);
              } else if (event.key === "Escape") {
                setOpen(false);
              }
            }}
            role="combobox"
            aria-label={label}
            aria-controls={listID}
            aria-expanded={open}
            aria-autocomplete="list"
            aria-activedescendant={open && visible[activeIndex] ? `${listID}-${activeIndex}` : undefined}
            placeholder={placeholder}
            maxLength={maxLength}
            disabled={disabled}
          />
          <button type="button" className="combobox-button" aria-label={`展开${label}`} tabIndex={-1} onClick={() => setOpen(!open)} disabled={disabled}>
            <ChevronDown size={14} />
          </button>
        </div>
      </PopoverPrimitive.Anchor>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          className="combobox-content"
          align="start"
          sideOffset={5}
          collisionPadding={12}
          onOpenAutoFocus={(event) => event.preventDefault()}
          onCloseAutoFocus={(event) => event.preventDefault()}
        >
          <div className="combobox-list" id={listID} role="listbox">
            {visible.length ? visible.map((option, index) => (
              <button
                type="button"
                className={cx("combobox-option", index === activeIndex && "combobox-option-active")}
                id={`${listID}-${index}`}
                role="option"
                aria-selected={option.value === value}
                onMouseMove={() => setActiveIndex(index)}
                onClick={() => choose(option)}
                key={option.value}
              >
                <span>{option.label}</span>
                {option.description ? <small>{option.description}</small> : null}
                {option.value === value ? <Check size={14} /> : null}
              </button>
            )) : <div className="combobox-empty">没有匹配项，可直接使用当前输入</div>}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}

interface FieldProps {
  label: string;
  className?: string;
  hint?: string;
  error?: string;
  required?: boolean;
  children: ReactNode;
}

export function Field({ label, className, hint, error, required, children }: FieldProps) {
  return (
    <label className={cx("field", className)}>
      <span className="field-label">
        {label}{required ? <span className="required">*</span> : null}
      </span>
      {children}
      {error ? <span className="field-error">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </label>
  );
}

interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  disabled?: boolean;
}

export function Switch({ checked, onChange, label, disabled }: SwitchProps) {
  return (
    <button
      type="button"
      className={cx("switch", checked && "switch-on")}
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={label}
      onClick={() => onChange(!checked)}
      disabled={disabled}
    >
      <span className="switch-thumb" />
    </button>
  );
}

type BadgeTone = "neutral" | "success" | "warning" | "danger" | "info";

export function Badge({ tone = "neutral", className, children, ...props }: HTMLAttributes<HTMLSpanElement> & { tone?: BadgeTone }) {
  return (
    <span className={cx("badge", `badge-${tone}`, className)} {...props}>
      {children}
    </span>
  );
}

interface ModalProps {
  open: boolean;
  title: string;
  description?: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  width?: "sm" | "md" | "lg";
}

export function Modal({ open, title, description, onClose, children, footer, width = "md" }: ModalProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onCloseRef.current();
    };
    document.body.classList.add("modal-open");
    document.addEventListener("keydown", onKeyDown);
    window.setTimeout(() => panelRef.current?.focus(), 0);
    return () => {
      document.body.classList.remove("modal-open");
      document.removeEventListener("keydown", onKeyDown);
      previous?.focus();
    };
  }, [open]);

  if (!open) return null;
  return (
    <div className="modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <div className={cx("modal", `modal-${width}`)} role="dialog" aria-modal="true" aria-labelledby="modal-title" tabIndex={-1} ref={panelRef}>
        <div className="modal-header">
          <div>
            <h2 id="modal-title">{title}</h2>
            {description ? <p>{description}</p> : null}
          </div>
          <IconButton label="关闭" onClick={onClose}><X size={18} /></IconButton>
        </div>
        <div className="modal-content">{children}</div>
        {footer ? <div className="modal-footer">{footer}</div> : null}
      </div>
    </div>
  );
}

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  description: string;
  confirmLabel?: string;
  loading?: boolean;
  danger?: boolean;
  onConfirm: () => void;
  onClose: () => void;
}

export function ConfirmDialog({ open, title, description, confirmLabel = "确认", loading, danger, onConfirm, onClose }: ConfirmDialogProps) {
  return (
    <Modal
      open={open}
      title={title}
      onClose={onClose}
      width="sm"
      footer={
        <>
          <Button onClick={onClose} disabled={loading}>取消</Button>
          <Button variant={danger ? "danger" : "primary"} onClick={onConfirm} loading={loading}>{confirmLabel}</Button>
        </>
      }
    >
      <p className="confirm-copy">{description}</p>
    </Modal>
  );
}

interface EmptyStateProps {
  title: string;
  description: string;
  action?: ReactNode;
  icon?: ReactNode;
}

export function EmptyState({ title, description, action, icon }: EmptyStateProps) {
  return (
    <div className="empty-state">
      <div className="empty-icon">{icon ?? <Inbox size={21} />}</div>
      <h3>{title}</h3>
      <p>{description}</p>
      {action ? <div className="empty-action">{action}</div> : null}
    </div>
  );
}

export function ErrorState({ title = "加载失败", message, retry }: { title?: string; message: string; retry?: () => void }) {
  return (
    <div className="empty-state error-state">
      <div className="empty-icon"><AlertCircle size={21} /></div>
      <h3>{title}</h3>
      <p>{message}</p>
      {retry ? <div className="empty-action"><Button onClick={retry}>重新加载</Button></div> : null}
    </div>
  );
}

export function PageLoader() {
  return (
    <div className="page-loader" aria-label="正在加载">
      <div className="skeleton skeleton-title" />
      <div className="skeleton-grid">
        <div className="skeleton skeleton-block" />
        <div className="skeleton skeleton-block" />
        <div className="skeleton skeleton-block" />
      </div>
      <div className="skeleton skeleton-table" />
    </div>
  );
}

interface PageHeaderProps {
  eyebrow?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
}

export function PageHeader({ eyebrow, title, description, actions }: PageHeaderProps) {
  return (
    <header className="page-header">
      <div>
        {eyebrow ? <span className="eyebrow">{eyebrow}</span> : null}
        <h1>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </header>
  );
}

type ToastTone = "success" | "error" | "info";
interface ToastItem { id: number; message: string; tone: ToastTone }
interface ToastContextValue { toast: (message: string, tone?: ToastTone) => void }
const ToastContext = createContext<ToastContextValue | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const idRef = useRef(0);
  const dismiss = useCallback((id: number) => setItems((current) => current.filter((item) => item.id !== id)), []);
  const toast = useCallback((message: string, tone: ToastTone = "info") => {
    const id = ++idRef.current;
    setItems((current) => [...current.slice(-3), { id, message, tone }]);
    window.setTimeout(() => dismiss(id), 4200);
  }, [dismiss]);
  const value = useMemo(() => ({ toast }), [toast]);
  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="toast-region" aria-live="polite" aria-atomic="false">
        {items.map((item) => (
          <div className={cx("toast", `toast-${item.tone}`)} key={item.id}>
            {item.tone === "success" ? <CheckCircle2 size={17} /> : item.tone === "error" ? <AlertCircle size={17} /> : <span className="toast-dot" />}
            <span>{item.message}</span>
            <IconButton label="关闭通知" onClick={() => dismiss(item.id)}><X size={15} /></IconButton>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastContextValue {
  const value = useContext(ToastContext);
  if (!value) throw new Error("useToast must be used inside ToastProvider");
  return value;
}
