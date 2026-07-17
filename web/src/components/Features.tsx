import type { CSSProperties } from 'react';
import { CodeBlock } from 'go-ui';
import type { Lib } from '../data';

export interface FeaturesProps {
  lib: Lib;
}

// Features renders the "Going further" integration snippet, the feature bullet
// list, and a pointer to the full API reference on the docs tab.
export function Features({ lib }: FeaturesProps) {
  return (
    <>
      <div className="sec-h" id={`${lib.id}-more`}><span className="bar" /><h3 style={{ margin: 0 }}>Going further</h3></div>
      <CodeBlock lang="go" html={lib.integrate} />

      <div className="sec-h" id={`${lib.id}-feat`}><span className="bar" /><h3 style={{ margin: 0 }}>Features</h3></div>
      <ul className="feat" style={{ '--lib-accent': lib.accent } as CSSProperties}>
        {lib.features.map((f, i) => <li key={i} dangerouslySetInnerHTML={{ __html: f }} />)}
      </ul>

      <div className="note">Full API reference &amp; runnable examples live on the <a href="#docs">docs tab</a>.</div>
    </>
  );
}
