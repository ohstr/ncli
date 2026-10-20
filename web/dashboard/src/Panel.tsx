import { useState, type ReactNode } from "react";

export interface MenuItem {
  label: string;
  run: () => void;
}

interface Props {
  id: string;
  title: string;
  hint?: string;
  desc?: string;
  className?: string;
  viewing?: boolean;
  onView?: (id: string | null) => void;
  menu?: MenuItem[];
  children: ReactNode;
}

// Panel is a Grafana-style box: a title bar with a hover menu, and a
// full-screen view of itself.
export function Panel({ id, title, hint, desc, className = "", viewing, onView, menu = [], children }: Props) {
  const [open, setOpen] = useState(false);
  const items: MenuItem[] = [
    ...(onView ? [{ label: viewing ? "Close view" : "View", run: () => onView(viewing ? null : id) }] : []),
    ...menu,
  ];
  return (
    <section className={`panel ${className}${viewing ? " viewing" : ""}`} data-panel={id}>
      <div className="panel-head">
        <h3>{title}</h3>
        {hint && <span className="hint">{hint}</span>}
        {desc && (
          <span className="info" title={desc} aria-label={desc}>
            i
          </span>
        )}
        <span className="spacer" />
        {items.length > 0 && (
          <div
            className="menu"
            onBlur={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
            }}
          >
            <button className="ghost icon" aria-label={`${title} menu`} aria-expanded={open} onClick={() => setOpen(!open)}>
              ⋮
            </button>
            {open && (
              <div className="menu-list" role="menu">
                {items.map((m) => (
                  <button
                    key={m.label}
                    role="menuitem"
                    className="ghost"
                    onClick={() => {
                      setOpen(false);
                      m.run();
                    }}
                  >
                    {m.label}
                  </button>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
      {children}
    </section>
  );
}

// download saves text as a file.
export function download(name: string, text: string, type = "text/csv") {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text], { type }));
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
