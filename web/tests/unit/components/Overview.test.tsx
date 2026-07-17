import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Overview } from '../../../src/components/Overview';
import { OBAN } from '../../../src/data';

describe('Overview', () => {
  beforeEach(() => {
    // VersionBadge fetches on mount; keep it pending.
    global.fetch = vi.fn().mockReturnValue(new Promise(() => {}));
  });

  it('renders the hero, blurb and every overview section', () => {
    const { container } = render(<Overview />);
    expect(container.querySelector('#view-overview')).not.toBeNull();
    expect(screen.getByRole('heading', { level: 2, name: /oban/i })).toBeInTheDocument();
    expect(screen.getByText(OBAN.blurb)).toBeInTheDocument();
    // The install / quick-start / comparison / going-further / features sections.
    for (const suffix of ['install', 'quick', 'cmp', 'more', 'feat']) {
      expect(container.querySelector(`#${OBAN.id}-${suffix}`)).not.toBeNull();
    }
    expect(screen.getByRole('heading', { name: 'Features' })).toBeInTheDocument();
    expect(container.querySelectorAll('ul.feat li').length).toBe(OBAN.features.length);
  });
});
