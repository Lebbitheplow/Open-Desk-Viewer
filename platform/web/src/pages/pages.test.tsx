import { describe, expect, it, vi, beforeEach } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';

import { apiClient, ApiError } from '../lib/api';
import { renderPage, renderRoute } from '../test/render';
import * as fx from '../test/fixtures';

import Dashboard from './Dashboard';
import Devices from './Devices';
import DeviceDetail from './DeviceDetail';
import Customers from './Customers';
import DeviceGroups from './DeviceGroups';
import SupportGroups from './SupportGroups';
import Users from './Users';
import EnrollmentTokens from './EnrollmentTokens';
import AuditLog from './AuditLog';
import Settings from './Settings';

// Every test here asserts a page renders data it fetched, not data it hardcodes.
// That is the property the previous portal lacked: App.tsx displayed zeros
// regardless of what the API said.
beforeEach(() => {
  vi.restoreAllMocks();
});

describe('Dashboard', () => {
  it('renders counts from the API rather than placeholders', async () => {
    vi.spyOn(apiClient, 'getDashboard').mockResolvedValue(fx.dashboard);

    renderPage(<Dashboard />);

    expect(await screen.findByText('42')).toBeInTheDocument();
    expect(screen.getByText('29')).toBeInTheDocument();
    expect(screen.getByText('18')).toBeInTheDocument();
    // The seven customers and five technicians come from the same payload.
    expect(screen.getByText('7')).toBeInTheDocument();
    expect(screen.getByText('5')).toBeInTheDocument();
  });

  it('lists the recent connections it was given', async () => {
    vi.spyOn(apiClient, 'getDashboard').mockResolvedValue(fx.dashboard);

    renderPage(<Dashboard />);

    expect(await screen.findByText('acme-hq-01')).toBeInTheDocument();
    expect(screen.getByText('tech1@example.com')).toBeInTheDocument();
  });

  it('shows the error rather than an empty dashboard when the fetch fails', async () => {
    vi.spyOn(apiClient, 'getDashboard').mockRejectedValue(new ApiError(500, 'database is down'));

    renderPage(<Dashboard />);

    expect(await screen.findByRole('alert')).toHaveTextContent('database is down');
  });
});

describe('Devices', () => {
  it('renders the fetched devices', async () => {
    vi.spyOn(apiClient, 'getDevices').mockResolvedValue(fx.page([fx.device, fx.discoveredDevice]));
    vi.spyOn(apiClient, 'getCustomers').mockResolvedValue(fx.page([fx.customer]));

    renderPage(<Devices />);

    expect(await screen.findByText('acme-hq-01')).toBeInTheDocument();
    expect(screen.getByText('unclaimed-01')).toBeInTheDocument();
    // "Acme" is also a customer filter option, so assert on the table cell.
    expect(screen.getByRole('cell', { name: 'Acme' })).toBeInTheDocument();
  });

  it('passes the search term to the API', async () => {
    const getDevices = vi
      .spyOn(apiClient, 'getDevices')
      .mockResolvedValue(fx.page([fx.device]));
    vi.spyOn(apiClient, 'getCustomers').mockResolvedValue(fx.page([fx.customer]));

    renderPage(<Devices />);
    await screen.findByText('acme-hq-01');

    const input = screen.getByLabelText('Search devices');
    input.focus();
    // Fire a change the way a user typing produces one.
    const { fireEvent } = await import('@testing-library/react');
    fireEvent.change(input, { target: { value: 'acme-br' } });

    await waitFor(() => {
      expect(getDevices).toHaveBeenCalledWith(expect.objectContaining({ q: 'acme-br' }));
    });
  });

  it('offers a claim action only for a discovered device', async () => {
    vi.spyOn(apiClient, 'getDevices').mockResolvedValue(fx.page([fx.device, fx.discoveredDevice]));
    vi.spyOn(apiClient, 'getCustomers').mockResolvedValue(fx.page([fx.customer]));

    renderPage(<Devices />);
    await screen.findByText('unclaimed-01');

    // One claim button for the one DISCOVERED device.
    expect(screen.getAllByRole('button', { name: 'Claim' })).toHaveLength(1);
  });

  it('reports a forbidden list as an access problem', async () => {
    vi.spyOn(apiClient, 'getDevices').mockRejectedValue(new ApiError(403, 'not authorised'));
    vi.spyOn(apiClient, 'getCustomers').mockRejectedValue(new ApiError(403, 'not authorised'));

    renderPage(<Devices />);

    expect(await screen.findByRole('alert')).toHaveTextContent('You do not have access');
  });
});

describe('DeviceDetail', () => {
  it('renders the device and its connections', async () => {
    vi.spyOn(apiClient, 'getDevice').mockResolvedValue(fx.deviceDetail);
    vi.spyOn(apiClient, 'getDeviceSessions').mockResolvedValue(fx.page([fx.session]));

    renderRoute('/devices/:id', <DeviceDetail />, `/devices/${fx.device.id}`);

    expect(await screen.findByRole('heading', { name: 'acme-hq-01' })).toBeInTheDocument();
    expect(screen.getByText('AMD Ryzen 7')).toBeInTheDocument();
    expect(screen.getByText('Acme HQ')).toBeInTheDocument();
    expect(await screen.findByText('Replaced the printer driver')).toBeInTheDocument();
  });

  it('says so when a device has never reported hardware', async () => {
    vi.spyOn(apiClient, 'getDevice').mockResolvedValue({ ...fx.deviceDetail, sysinfo: undefined });
    vi.spyOn(apiClient, 'getDeviceSessions').mockResolvedValue(fx.page([]));

    renderRoute('/devices/:id', <DeviceDetail />, `/devices/${fx.device.id}`);

    expect(
      await screen.findByText('This device has not reported its hardware yet.'),
    ).toBeInTheDocument();
  });

  // The connection password. Reading it is audited server-side, so the page
  // must not fetch it as part of loading the device.
  it('does not fetch the connection password until it is asked for', async () => {
    vi.spyOn(apiClient, 'getDevice').mockResolvedValue(fx.deviceDetail);
    vi.spyOn(apiClient, 'getDeviceSessions').mockResolvedValue(fx.page([]));
    const reveal = vi.spyOn(apiClient, 'getDevicePassword').mockResolvedValue(fx.devicePassword);

    renderRoute('/devices/:id', <DeviceDetail />, `/devices/${fx.device.id}`);
    await screen.findByRole('heading', { name: 'acme-hq-01' });

    expect(reveal).not.toHaveBeenCalled();
    expect(screen.queryByText(fx.devicePassword.password)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: 'Show password' }));

    expect(await screen.findByText(fx.devicePassword.password)).toBeInTheDocument();
    expect(reveal).toHaveBeenCalledTimes(1);
  });

  // The property the whole feature turns on: a rotation the device has not
  // confirmed must not read as if access had already been withdrawn.
  it('says a rotation is still pending until the device confirms it', async () => {
    vi.spyOn(apiClient, 'getDevice').mockResolvedValue(fx.deviceDetail);
    vi.spyOn(apiClient, 'getDeviceSessions').mockResolvedValue(fx.page([]));
    vi.spyOn(apiClient, 'rotateDevicePassword').mockResolvedValue({
      ...fx.devicePassword,
      password: 'qrstuvwxyz234567',
      version: 3,
      applied_version: 2,
      applied: false,
      applied_at: null,
    });

    renderRoute('/devices/:id', <DeviceDetail />, `/devices/${fx.device.id}`);
    await screen.findByRole('heading', { name: 'acme-hq-01' });

    await userEvent.click(screen.getByRole('button', { name: 'Rotate' }));

    expect(await screen.findByText('qrstuvwxyz234567')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent(
      'still accepting the previous one (version 2)',
    );
  });

  it('shows the refusal when a technician is not allowed to rotate', async () => {
    vi.spyOn(apiClient, 'getDevice').mockResolvedValue(fx.deviceDetail);
    vi.spyOn(apiClient, 'getDeviceSessions').mockResolvedValue(fx.page([]));
    vi.spyOn(apiClient, 'rotateDevicePassword').mockRejectedValue(
      new ApiError(403, 'administrator access required'),
    );

    renderRoute('/devices/:id', <DeviceDetail />, `/devices/${fx.device.id}`);
    await screen.findByRole('heading', { name: 'acme-hq-01' });

    await userEvent.click(screen.getByRole('button', { name: 'Rotate' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('administrator access required');
  });
});

describe('Customers', () => {
  it('renders the fetched customers with their device counts', async () => {
    vi.spyOn(apiClient, 'getCustomers').mockResolvedValue(fx.page([fx.customer]));

    renderPage(<Customers />);

    expect(await screen.findByText('Acme')).toBeInTheDocument();
    expect(screen.getByText('ACME')).toBeInTheDocument();
    expect(screen.getByText('12')).toBeInTheDocument();
  });
});

describe('DeviceGroups', () => {
  it('renders the fetched groups', async () => {
    vi.spyOn(apiClient, 'getDeviceGroups').mockResolvedValue(fx.page([fx.deviceGroup]));

    renderPage(<DeviceGroups />);

    expect(await screen.findByText('Acme HQ')).toBeInTheDocument();
  });
});

describe('SupportGroups', () => {
  it('renders the fetched support groups', async () => {
    vi.spyOn(apiClient, 'getSupportGroups').mockResolvedValue(fx.page([fx.supportGroup]));

    renderPage(<SupportGroups />);

    expect(await screen.findByText('Team One')).toBeInTheDocument();
  });

  it('shows technicians and grants once a group is selected', async () => {
    vi.spyOn(apiClient, 'getSupportGroups').mockResolvedValue(fx.page([fx.supportGroup]));
    vi.spyOn(apiClient, 'getSupportGroup').mockResolvedValue(fx.supportGroupDetail);
    vi.spyOn(apiClient, 'getUsers').mockResolvedValue(fx.page([fx.user]));
    vi.spyOn(apiClient, 'getDeviceGroups').mockResolvedValue(fx.page([fx.deviceGroup]));

    renderPage(<SupportGroups />);

    const { fireEvent } = await import('@testing-library/react');
    fireEvent.click(await screen.findByRole('button', { name: /Team One/ }));

    expect(await screen.findByText('Tech One')).toBeInTheDocument();
    expect(screen.getByLabelText('Grant a device group')).toBeInTheDocument();
  });
});

describe('Users', () => {
  it('renders users with their roles and status', async () => {
    vi.spyOn(apiClient, 'getUsers').mockResolvedValue(fx.page([fx.user]));

    renderPage(<Users />);

    expect(await screen.findByText('Tech One')).toBeInTheDocument();
    expect(screen.getByText('tech1@example.com')).toBeInTheDocument();
    expect(screen.getByText('Technician ×')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
  });

  it('calls the API to deactivate a user', async () => {
    vi.spyOn(apiClient, 'getUsers').mockResolvedValue(fx.page([fx.user]));
    const setActive = vi
      .spyOn(apiClient, 'setUserActive')
      .mockResolvedValue({ ...fx.user, active: false });

    renderPage(<Users />);

    const { fireEvent } = await import('@testing-library/react');
    fireEvent.click(await screen.findByRole('button', { name: 'Deactivate' }));

    await waitFor(() => expect(setActive).toHaveBeenCalledWith(fx.user.id, false));
  });

  // The role buttons come from the API's list, not from a constant in this
  // file. The constant used to say "Manager", which is not a role that exists,
  // so the button was always there and always failed; and it omitted
  // "Read Only", so that role could not be granted at all.
  it('offers the roles the deployment actually has', async () => {
    vi.spyOn(apiClient, 'getUsers').mockResolvedValue(fx.page([fx.user]));
    vi.spyOn(apiClient, 'getSettings').mockResolvedValue(fx.settings);

    renderPage(<Users />);

    expect(await screen.findByRole('option', { name: 'Read Only' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Support Manager' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Administrator' })).toBeInTheDocument();
    // Held already, so it is not offered for granting.
    expect(screen.queryByRole('option', { name: 'Technician' })).not.toBeInTheDocument();
    // And the role that never existed.
    expect(screen.queryByRole('option', { name: 'Manager' })).not.toBeInTheDocument();
  });
});

describe('EnrollmentTokens', () => {
  it('asks for a customer before listing tokens', async () => {
    vi.spyOn(apiClient, 'getCustomers').mockResolvedValue(fx.page([fx.customer]));

    renderPage(<EnrollmentTokens />);

    expect(
      await screen.findByText('Choose a customer to list its enrollment tokens.'),
    ).toBeInTheDocument();
  });

  it('shows the plaintext token once after issuing it', async () => {
    vi.spyOn(apiClient, 'getCustomers').mockResolvedValue(fx.page([fx.customer]));
    vi.spyOn(apiClient, 'getEnrollmentTokens').mockResolvedValue(fx.page([fx.enrollmentToken]));
    vi.spyOn(apiClient, 'createEnrollmentToken').mockResolvedValue({
      ...fx.enrollmentToken,
      token: 'plaintext-token-value',
    });

    renderPage(<EnrollmentTokens />);

    const { fireEvent } = await import('@testing-library/react');
    const select = await screen.findByLabelText('Customer');
    fireEvent.change(select, { target: { value: fx.customer.id } });
    fireEvent.click(screen.getByRole('button', { name: 'Issue token' }));

    expect(await screen.findByText('plaintext-token-value')).toBeInTheDocument();
    expect(screen.getByText(/not shown again/)).toBeInTheDocument();
  });
});

describe('AuditLog', () => {
  it('renders connection sessions including the operator note', async () => {
    vi.spyOn(apiClient, 'getAuditSessions').mockResolvedValue(fx.page([fx.session]));

    renderPage(<AuditLog />);

    expect(await screen.findByText('Replaced the printer driver')).toBeInTheDocument();
    expect(screen.getByText('acme-hq-01')).toBeInTheDocument();
  });

  it('switches to administrative changes', async () => {
    vi.spyOn(apiClient, 'getAuditSessions').mockResolvedValue(fx.page([fx.session]));
    vi.spyOn(apiClient, 'getAuditEvents').mockResolvedValue(fx.page([fx.auditEvent]));

    renderPage(<AuditLog />);
    await screen.findByText('Replaced the printer driver');

    const { fireEvent } = await import('@testing-library/react');
    fireEvent.click(screen.getByRole('button', { name: 'Changes' }));

    expect(await screen.findByText('device.reassigned')).toBeInTheDocument();
    expect(screen.getByText('admin@example.com')).toBeInTheDocument();
  });
});

describe('Settings', () => {
  it('renders values from the API and names the variable behind each', async () => {
    vi.spyOn(apiClient, 'getSettings').mockResolvedValue(fx.settings);

    renderPage(<Settings />);

    expect(await screen.findByText('90 days')).toBeInTheDocument();
    expect(screen.getByText('300 seconds')).toBeInTheDocument();
    expect(screen.getByText('AUDIT_RETENTION_DAYS')).toBeInTheDocument();
    expect(screen.getByText(/read-only in this release/)).toBeInTheDocument();
  });
});
