import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QuickStart } from '../../../src/components/QuickStart';
import { OBAN } from '../../../src/data';

describe('QuickStart', () => {
  it('renders the Quick start heading and highlighted Go snippet', () => {
    const { container } = render(<QuickStart lib={OBAN} />);
    expect(container.querySelector(`#${OBAN.id}-quick`)).not.toBeNull();
    expect(screen.getByRole('heading', { name: 'Quick start' })).toBeInTheDocument();
    // The snippet mentions oban.NewJob.
    expect(container.textContent).toContain('oban.NewJob');
  });
});
