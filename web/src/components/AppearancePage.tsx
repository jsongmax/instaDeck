import { useAppStore } from '../store/useAppStore'
import { SkinCardsGrid } from './Dialogs'

export function AppearancePage() {
  const termSize = useAppStore((s) => s.termSize)
  const setTermSize = useAppStore((s) => s.setTermSize)

  return (
    <>
      <div className="page-head">
        <h1>外观主题</h1>
      </div>
      <section className="card card-wide">
        <h2>主题</h2>
        <SkinCardsGrid />
        <p className="muted" style={{ margin: 0, fontSize: '12.5px' }}>
          面板和已打开的终端窗口会一起切换。
        </p>
      </section>
      <section className="card card-wide">
        <h2>终端</h2>
        <label className="lbl" htmlFor="tsize" style={{ maxWidth: '220px' }}>
          字号
          <select
            className="field"
            id="tsize"
            value={termSize}
            onChange={(e) => setTermSize(Number(e.target.value), true)}
          >
            {[12, 13, 14, 15, 16].map((n) => (
              <option key={n} value={n}>
                {n} px
              </option>
            ))}
          </select>
        </label>
      </section>
    </>
  )
}
