defmodule Phoenix.Component do
  @moduledoc false

  defmacro __using__(_opts) do
    quote do
      import Phoenix.Component, only: [sigil_H: 2]
    end
  end

  defmacro sigil_H({:<<>>, _meta, [contents]}, _modifiers), do: contents
end
