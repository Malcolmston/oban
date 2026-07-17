import { DocsApp } from 'go-ui';
import { OBAN } from '../data';

// DocsView is the "docs" tab. It renders the full, package-by-package Go API
// reference inline via the shared `DocsApp`, which fetches the generated
// `doc.json` (emitted by docs/gen) and shows a package sidebar + package view,
// hash-routable by import path. A secondary link points at the raw generated
// static HTML (`./api/`), published alongside this site under /oban/api/.
//
// `doc.json` is served at `<base>/doc.json`. If it is missing, DocsApp degrades
// gracefully (it renders an inline error/loading state rather than crashing).
export function DocsView() {
  const lib = OBAN;
  return (
    <section className="view active" id="view-docs">
      <div className="sec-h"><span className="bar" /><h2 style={{ margin: 0 }}>API documentation</h2></div>
      <p className="muted">The full API reference for <code>{lib.pkg}</code> (package <code>oban</code>) is generated from the source with a dependency-free <code>go/doc</code> tool (committed in the repo under <code>docs/gen</code>) and rendered inline below.</p>
      <div className="actions">
        <a className="pill b" href="./api/"><i className="fa-solid fa-book" /> Open the raw generated HTML</a>
        <a className="pill b" href={lib.repo} target="_blank" rel="noopener"><i className="fa-brands fa-github" />&nbsp;Source on GitHub</a>
      </div>

      <DocsApp url={`${import.meta.env.BASE_URL}doc.json`} />
    </section>
  );
}
