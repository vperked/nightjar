import type { ReactNode } from 'react';
import { useCanEdit } from '../api/ConfigContext';

// Editable shows its children to the admin only; a read-only viewer sees the fallback, or nothing.
export function Editable({ children, fallback = null }: { children: ReactNode; fallback?: ReactNode }) {
  return <>{useCanEdit() ? children : fallback}</>;
}
