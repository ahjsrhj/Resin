import { useQuery } from "@tanstack/react-query";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { listNodes } from "../nodes/api";
import type { NodeSummary } from "../nodes/types";

type SubscriptionChainNodeSelectProps = {
  id?: string;
  value: string;
  invalid?: boolean;
  disabled?: boolean;
  onChange: (value: string) => void;
};

function summarizeNode(node: NodeSummary, t: ReturnType<typeof useI18n>["t"]): string {
  const fallbackTag = node.tags[0]
    ? `${node.tags[0].subscription_name}/${node.tags[0].tag}`.replace(/^\/|\/$/g, "")
    : "";
  const displayTag = node.display_tag?.trim() || fallbackTag;
  const shortHash = node.node_hash.slice(0, 8);
  let status = t("可用");
  if (!node.enabled) {
    status = t("已禁用");
  } else if (node.circuit_open_since) {
    status = t("熔断");
  }

  const label = displayTag || t("节点 {{hash}}", { hash: shortHash });
  return `${label} · ${shortHash} · ${status}`;
}

export function SubscriptionChainNodeSelect({
  id,
  value,
  invalid,
  disabled,
  onChange,
}: SubscriptionChainNodeSelectProps) {
  const { t } = useI18n();
  const nodesQuery = useQuery({
    queryKey: ["subscription-chain-node-options"],
    queryFn: () => listNodes({
      limit: 500,
      offset: 0,
      sort_by: "tag",
      sort_order: "asc",
      has_outbound: true,
    }),
    staleTime: 30_000,
  });

  const nodes = nodesQuery.data?.items ?? [];
  const hasSelectedNode = value !== "" && nodes.some((item) => item.node_hash === value);

  return (
    <>
      <Select
        id={id}
        invalid={invalid}
        disabled={disabled || nodesQuery.isLoading}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">{nodesQuery.isLoading ? t("正在加载节点...") : t("未指定代理链节点")}</option>
        {!hasSelectedNode && value ? (
          <option value={value}>{t("当前配置节点不可用 · {{hash}}", { hash: value.slice(0, 8) })}</option>
        ) : null}
        {nodes.map((item) => (
          <option key={item.node_hash} value={item.node_hash}>
            {summarizeNode(item, t)}
          </option>
        ))}
      </Select>
      {nodesQuery.isError ? (
        <p className="field-error">{t("代理链节点列表加载失败")}</p>
      ) : (
        <p className="muted" style={{ marginTop: 4, fontSize: 12 }}>
          {t("代理链节点只负责中间转发，不作为最终出口。")}
        </p>
      )}
    </>
  );
}
