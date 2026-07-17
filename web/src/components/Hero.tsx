import type { CSSProperties } from 'react';
import { VersionBadge, hx, ghrepo } from 'go-ui';
import type { Lib } from '../data';

export interface HeroProps {
  lib: Lib;
}

// Hero renders the library title card: icon, name, package path, tagline, and
// the action pills (GitHub, API docs, live version badge, upstream port).
export function Hero({ lib }: HeroProps) {
  return (
    <div className="libhero" style={{ '--lib-soft': hx(lib.accent, '1f'), '--lib-accent': lib.accent } as CSSProperties}>
      <div className="row">
        <span className="mono" dangerouslySetInnerHTML={{ __html: lib.icon }} />
        <div style={{ flex: 1, minWidth: 220 }}>
          <h2>{lib.name} <span className="muted" style={{ fontWeight: 400, fontSize: '1rem' }}>for Go</span></h2>
          <div className="pkg mono">{lib.pkg}</div>
          <p className="tagline">{lib.tagline}</p>
        </div>
      </div>
      <div className="actions">
        <a className="pill b" href={lib.repo} target="_blank" rel="noopener"><i className="fa-brands fa-github" />&nbsp;GitHub</a>
        <a className="pill b" href="#docs"><i className="fa-solid fa-book" /> API docs</a>
        <VersionBadge repo={ghrepo(lib)} href={`${lib.repo}/releases`} />
        <span className="pill">ports <b style={{ color: 'var(--fg)', marginLeft: '.25rem' }}>{lib.node}</b></span>
      </div>
    </div>
  );
}
