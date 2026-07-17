import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Features } from '../../../src/components/Features';
import { OBAN } from '../../../src/data';

describe('Features', () => {
  it('renders the going-further snippet, the features list and a docs pointer', () => {
    const { container } = render(<Features lib={OBAN} />);
    expect(container.querySelector(`#${OBAN.id}-more`)).not.toBeNull();
    expect(screen.getByRole('heading', { name: 'Going further' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Features' })).toBeInTheDocument();
    expect(container.querySelectorAll('ul.feat li').length).toBe(OBAN.features.length);
    const docs = screen.getByRole('link', { name: /docs tab/ });
    expect(docs).toHaveAttribute('href', '#docs');
  });
});
