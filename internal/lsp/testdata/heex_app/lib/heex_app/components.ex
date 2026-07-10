defmodule HeexApp.Components do
  use Phoenix.Component

  def remote_card(assigns) do
    ~H"""
    <article>{@title}</article>
    """
  end

  def wrapper(assigns), do: ~H"<section>slot</section>"
end
