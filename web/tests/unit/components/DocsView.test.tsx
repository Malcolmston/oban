import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DocsView } from '../../../src/components/DocsView';
import type { DocIndex } from 'go-ui';

// A minimal DocIndex the stubbed fetch returns for DocsApp's doc.json request.
const DOC_INDEX: DocIndex = {
  module: 'github.com/malcolmston/oban',
  packages: [
    {
      importPath: 'github.com/malcolmston/oban',
      name: 'oban',
      synopsis: 'Package oban is a standard-library-only background job system for Go.',
      doc: 'Package oban is a standard-library-only background job system for Go.',
      consts: [],
      vars: [],
      types: [
        {
          name: 'Job',
          signature: 'type Job struct{}',
          doc: 'Job is a unit of background work.',
          consts: [],
          vars: [],
          funcs: [],
          methods: [],
        },
      ],
      funcs: [{ name: 'NewJob', signature: 'func NewJob(worker string, args any, opts ...JobOption) (*Job, error)', doc: 'NewJob builds a job for the named worker.' }],
    },
  ],
};

describe('DocsView', () => {
  beforeEach(() => {
    // DocsApp fetches doc.json; return the small index.
    global.fetch = vi.fn((input: RequestInfo | URL) => {
      if (String(input).includes('doc.json')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(DOC_INDEX) } as Response);
      }
      return new Promise<Response>(() => {});
    }) as unknown as typeof fetch;
  });

  it('renders the inline React API reference from the fetched doc.json', async () => {
    const { container } = render(<DocsView />);
    expect(container.querySelector('#view-docs')).not.toBeNull();
    expect(
      screen.getByRole('heading', { level: 2, name: /API documentation/ }),
    ).toBeInTheDocument();

    // DocsApp fetches asynchronously, then renders the package view + symbols.
    expect(await screen.findByRole('heading', { name: /package oban/ })).toBeInTheDocument();
    expect(container.querySelector('#sym-NewJob'), 'func NewJob symbol card').not.toBeNull();
    expect(container.querySelector('#sym-Job'), 'type Job symbol card').not.toBeNull();

    // The secondary link to the raw generated static HTML remains.
    expect(screen.getByRole('link', { name: /Open the raw generated HTML/ })).toHaveAttribute('href', './api/');
  });
});
