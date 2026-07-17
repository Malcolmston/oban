import { CodeBlock, hi } from 'go-ui';
import type { Lib } from '../data';

export interface QuickStartProps {
  lib: Lib;
}

// QuickStart shows the minimal Go usage snippet, syntax-highlighted.
export function QuickStart({ lib }: QuickStartProps) {
  return (
    <>
      <div className="sec-h" id={`${lib.id}-quick`}><span className="bar" /><h3 style={{ margin: 0 }}>Quick start</h3></div>
      <CodeBlock lang="main.go" html={hi(lib.go_code)} />
    </>
  );
}
