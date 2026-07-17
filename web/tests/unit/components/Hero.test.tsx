import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Hero } from '../../../src/components/Hero';
import { OBAN } from '../../../src/data';

describe('Hero', () => {
  beforeEach(() => {
    // VersionBadge fetches on mount; keep it pending so the hero renders cleanly.
    global.fetch = vi.fn().mockReturnValue(new Promise(() => {}));
  });

  it('renders the name, package path and tagline', () => {
    render(<Hero lib={OBAN} />);
    expect(screen.getByRole('heading', { level: 2, name: /oban/i })).toBeInTheDocument();
    expect(screen.getByText(OBAN.pkg)).toBeInTheDocument();
    expect(screen.getByText(OBAN.tagline)).toBeInTheDocument();
  });

  it('renders the GitHub link opening in a new tab', () => {
    render(<Hero lib={OBAN} />);
    const github = screen.getByRole('link', { name: /GitHub/ });
    expect(github).toHaveAttribute('href', OBAN.repo);
    expect(github).toHaveAttribute('target', '_blank');
    expect(github).toHaveAttribute('rel', expect.stringContaining('noopener'));
  });
});
