import { KpiRow } from "@/components/monitor/kpi-row"
import { BalanceOverview } from "@/components/monitor/balance-overview"
import { MultiplierChanges } from "@/components/monitor/multiplier-changes"
import { ChannelCards } from "@/components/monitor/channel-cards"
import { BottomPanels } from "@/components/monitor/bottom-panels"

export default function Page() {
  return (
    <>
      <KpiRow />

      <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.65fr)_minmax(320px,0.85fr)]">
        <div className="min-w-0">
          <BalanceOverview />
        </div>
        <div className="min-w-0">
          <MultiplierChanges />
        </div>
      </div>

      <ChannelCards />

      <BottomPanels />
    </>
  )
}
