import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { NodeVsGo } from '../../../src/components/NodeVsGo';
import { OBAN } from '../../../src/data';

describe('NodeVsGo', () => {
  it('renders the comparison heading and both Elixir and Go columns', () => {
    const { container } = render(<NodeVsGo lib={OBAN} />);
    expect(container.querySelector(`#${OBAN.id}-cmp`)).not.toBeNull();
    expect(screen.getByText('Elixir')).toBeInTheDocument();
    expect(screen.getByText('Go')).toBeInTheDocument();
    expect(container.querySelectorAll('.compare .code').length).toBe(2);
  });
});
