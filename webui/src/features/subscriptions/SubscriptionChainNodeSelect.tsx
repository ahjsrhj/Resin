import { useQuery } from "@tanstack/react-query";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { listPlatforms } from "../platforms/api";
import type { Platform } from "../platforms/types";

type SubscriptionChainPlatformSelectProps = {
  id?: string;
  value: string;
  invalid?: boolean;
  disabled?: boolean;
  onChange: (value: string) => void;
};

function summarizePlatform(platform: Platform, t: ReturnType<typeof useI18n>["t"]): string {
  return `${platform.name} · ${t("{{count}} 个可路由节点", { count: platform.routable_node_count })}`;
}

export function SubscriptionChainPlatformSelect({
  id,
  value,
  invalid,
  disabled,
  onChange,
}: SubscriptionChainPlatformSelectProps) {
  const { t } = useI18n();
  const platformsQuery = useQuery({
    queryKey: ["subscription-chain-platform-options"],
    queryFn: () => listPlatforms({
      limit: 500,
      offset: 0,
    }),
    staleTime: 30_000,
  });

  const platforms = platformsQuery.data?.items ?? [];
  const hasSelectedPlatform = value !== "" && platforms.some((item) => item.id === value);

  return (
    <>
      <Select
        id={id}
        invalid={invalid}
        disabled={disabled || platformsQuery.isLoading}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">{platformsQuery.isLoading ? t("正在加载平台...") : t("未指定代理链平台")}</option>
        {!hasSelectedPlatform && value ? (
          <option value={value}>{t("当前配置平台不可用 · {{id}}", { id: value.slice(0, 8) })}</option>
        ) : null}
        {platforms.map((item) => (
          <option key={item.id} value={item.id}>
            {summarizePlatform(item, t)}
          </option>
        ))}
      </Select>
      {platformsQuery.isError ? (
        <p className="field-error">{t("代理链平台列表加载失败")}</p>
      ) : (
        <p className="muted" style={{ marginTop: 4, fontSize: 12 }}>
          {t("代理链平台会为当前订阅动态选择前置节点。")}
        </p>
      )}
    </>
  );
}
