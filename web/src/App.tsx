import { useEffect } from 'react';
import type { ReactNode } from 'react';
import { Layout, useHashTab } from 'go-ui';
import type { Tab } from 'go-ui';
import { Overview } from './components/Overview';
import { ReleasesView } from './components/ReleasesView';
import { DocsView } from './components/DocsView';

const TABS: Tab[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'releases', label: 'Releases' },
  { id: 'docs', label: 'Docs' },
];
const TAB_IDS = TABS.map((t) => t.id);

// App is the top-level composition: hash-routed tabs wrapped in the shared
// Layout, switching which view renders.
export function App() {
  const [active, go] = useHashTab(TAB_IDS, 'overview');

  // Reveal-on-mount: make any .reveal blocks in the active view visible.
  useEffect(() => {
    const t = setTimeout(() => document.querySelectorAll('.reveal').forEach((el) => el.classList.add('in')), 30);
    return () => clearTimeout(t);
  }, [active]);

  const brand = { title: 'malcolmston', sub: '/oban', initials: 'ob', href: '#overview' };
  const footer: ReactNode = (
    <div>
      <span className="grad-text" style={{ fontWeight: 700 }}>malcolmston/oban</span> — background job processing for Go, standard library only.
      <div className="small dim" style={{ marginTop: '.4rem' }}>MIT licensed · inspired by Elixir's Oban and Ruby's Sidekiq</div>
    </div>
  );

  return (
    <Layout brand={brand} tabs={TABS} active={active} onNav={go} github="https://github.com/malcolmston/oban" footer={footer}>
      {active === 'overview' && <Overview />}
      {active === 'releases' && <ReleasesView />}
      {active === 'docs' && <DocsView />}
    </Layout>
  );
}
