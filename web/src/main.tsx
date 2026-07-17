import { createRoot } from 'react-dom/client';
import { applyStoredTheme } from 'go-ui';
import { App } from './App';

applyStoredTheme();
createRoot(document.getElementById('root')!).render(<App />);
