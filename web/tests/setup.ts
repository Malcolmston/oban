// Global test setup: register jest-dom matchers (toBeInTheDocument, etc.) and
// reset DOM/mocks between tests so component tests stay isolated.
import '@testing-library/jest-dom/vitest';
import { afterEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
