import { Streamdown } from 'streamdown'

/*
  Обёртка над Streamdown (streaming markdown, T-14): единое место,
  чтобы при необходимости поменять реализацию рендера в одном файле.
  isAnimating=false — текст у нас приходит готовыми событиями, а не
  посимвольным стримом, typewriter-анимация не нужна.
*/
export function Markdown({ children }: { children: string }) {
  return (
    <div className="markdown-body text-sm leading-relaxed text-zinc-200">
      <Streamdown isAnimating={false}>{children}</Streamdown>
    </div>
  )
}
