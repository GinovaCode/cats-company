import React from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '../views/workspace-styles';
import { applyDocumentTheme } from '../utils/theme-access';
import ChatBubblesDemo from './chat-bubbles-demo';

if (import.meta.env.DEV) {
  applyDocumentTheme('liquid');
  createRoot(document.getElementById('root')).render(<ChatBubblesDemo />);
}
