import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { UpdateSpec } from '../src/api/types';
import { ToastProvider } from '../src/components/Toasts';
import { UpdateForm } from '../src/pages/UpdateForm';

const show = (initial: UpdateSpec, commandsEditable: boolean) =>
  render(
    <ToastProvider>
      <UpdateForm initial={initial} commandsEditable={commandsEditable} save={async () => ({ target: {} as never })} onSaved={() => {}} onCancel={() => {}} />
    </ToastProvider>,
  );

describe('Update form without an access token', () => {
  it('locks where a release comes from and the service it restarts, as the server does', () => {
    show({ id: 'app', name: 'App', kind: 'release', auto: false, paused: false, repo: 'owner/app', unit: 'app.service', restart: true }, false);
    for (const label of ['GitHub repository', 'Tag prefix', 'Version command', 'Update command', 'systemd unit']) expect(screen.getByLabelText(label)).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: /Restart it after updating/ })).toBeDisabled();
    // What decides nothing that runs stays open.
    expect(screen.getByLabelText('Name')).toBeEnabled();
    expect(screen.getByRole('checkbox', { name: /Update automatically/ })).toBeEnabled();
  });

  it('locks an app’s address', () => {
    show({ id: 'sonarr', name: 'Sonarr', kind: 'arr', auto: false, paused: false, url: 'http://127.0.0.1:8989' }, false);
    expect(screen.getByLabelText('URL')).toBeDisabled();
    expect(screen.getByLabelText('API key')).toBeEnabled();
  });

  it('leaves them all open with a token', () => {
    show({ id: 'app', name: 'App', kind: 'release', auto: false, paused: false, repo: 'owner/app', unit: 'app.service' }, true);
    expect(screen.getByLabelText('GitHub repository')).toBeEnabled();
    expect(screen.getByLabelText('systemd unit')).toBeEnabled();
  });
});
