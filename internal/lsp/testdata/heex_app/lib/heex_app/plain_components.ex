defmodule HeexApp.PlainComponents do
  defmacro sigil_H({:<<>>, _meta, [contents]}, _modifiers), do: contents

  def render(assigns), do: ~H"<.isolated_card />"
  defp isolated_card(assigns), do: ~H"<span>isolated</span>"
end
