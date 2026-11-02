import { createContext, useContext } from 'react';
import type { AppConfig } from './types';

// ConfigContext shares /api/config (which features are on) with the pages, once loaded.
export const ConfigContext = createContext<AppConfig | undefined>(undefined);

export const useConfig = () => useContext(ConfigContext);
