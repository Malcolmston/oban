import { OBAN } from '../data';
import { Hero } from './Hero';
import { Install } from './Install';
import { QuickStart } from './QuickStart';
import { NodeVsGo } from './NodeVsGo';
import { Features } from './Features';

// Overview is the landing tab: the library hero + blurb, an on-this-page nav,
// then the install / quick-start / comparison / features sections.
export function Overview() {
  const lib = OBAN;
  const idb = lib.id;
  return (
    <section className="view active" id="view-overview">
      <Hero lib={lib} />

      <p className="muted">{lib.blurb}</p>
      <div className="onthispage">
        <a href={`#${idb}-install`}>Install</a>
        <a href={`#${idb}-quick`}>Quick start</a>
        <a href={`#${idb}-cmp`}>Elixir → Go</a>
        <a href={`#${idb}-more`}>Going further</a>
        <a href={`#${idb}-feat`}>Features</a>
      </div>

      <Install lib={lib} />
      <QuickStart lib={lib} />
      <NodeVsGo lib={lib} />
      <Features lib={lib} />
    </section>
  );
}
