import type { ReactNode } from "react";
import { formatDate } from "../lib/format";
import { useI18n } from "../i18n";
import type { DeviceBinding } from "../types";
import { EmptyRow } from "./ui";

// Devices bound to an account under its device limit, oldest binding first.
// One row per device; the actions column is left to callers that can act on a
// binding, so the table stays read-only for operators with accounts.read only.
export function DeviceBindingTable({
  rows,
  actions
}: {
  rows: DeviceBinding[];
  actions?: (row: DeviceBinding) => ReactNode;
}) {
  const { t } = useI18n();
  const columns = actions ? 6 : 5;

  return (
    <div className="table-wrap">
      <table className="data-table device-binding-table">
        <thead>
          <tr>
            <th>{t("deviceBinding.device")}</th>
            <th>{t("deviceBinding.platform")}</th>
            <th>{t("deviceBinding.deviceID")}</th>
            <th>{t("deviceBinding.boundAt")}</th>
            <th>{t("deviceBinding.lastUsedAt")}</th>
            {actions && <th className="device-actions-head">{t("common.actions")}</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.device_id}>
              <td className="device-text">{row.device_model || "-"}</td>
              <td className="device-text">
                {row.platform || "-"}
                {row.app_version && <span className="muted"> · {t("deviceBinding.appVersion")} {row.app_version}</span>}
              </td>
              <td className="mono">{row.device_id}</td>
              <td>{formatDate(row.bound_at) || "-"}</td>
              <td>{formatDate(row.last_used_at) || "-"}</td>
              {actions && (
                <td className="device-actions-cell">
                  <div className="device-actions">{actions(row)}</div>
                </td>
              )}
            </tr>
          ))}
          {rows.length === 0 && <EmptyRow colSpan={columns} />}
        </tbody>
      </table>
    </div>
  );
}
